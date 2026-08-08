package main

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const observeProxyUsage = `Usage:
  velociportal setup observe-proxy [options]

Options:
  --env-file FILE   Atomically update only TRUSTED_PROXY_CIDR in FILE (default ".env")
  --listen ADDR     Temporary observer listen address (default "127.0.0.1:8080")
  --timeout DURATION
                    Stop waiting after this positive Go duration (default "2m")
  -h, --help        Show this help

The observer uses a crypto-random one-time path and the connection RemoteAddr
only. Forwarded and identity headers are ignored. The observed exact /32 or /128
is shown for explicit confirmation before the environment file is changed.
`

const proxyObservationPathPrefix = "/_velociportal/observe/"

type proxyObserverDependencies struct {
	random          io.Reader
	listen          func(network, address string) (net.Listener, error)
	shutdownTimeout time.Duration
}

type proxyObservationHandler struct {
	path     string
	observed chan<- string
	claimed  atomic.Bool
}

func runObserveProxyCommandWithDependencies(args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies proxyObserverDependencies) int {
	stdin, stdout, stderr = normalizeSetupIO(stdin, stdout, stderr)

	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, observeProxyUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %s cannot be combined with other arguments\n\n%s", arg, observeProxyUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("setup observe-proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := ".env"
	listenAddr := "127.0.0.1:8080"
	timeoutText := "2m"
	flags.Func("env-file", "environment file to update", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.Func("listen", "temporary observer listen address", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("address must not be empty")
		}
		listenAddr = value
		return nil
	})
	flags.Func("timeout", "observation timeout", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("duration must not be empty")
		}
		timeoutText = value
		return nil
	})
	flags.Usage = func() {}

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n\n%s", err, observeProxyUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy does not accept positional arguments\n\n%s", observeProxyUsage)
		return 2
	}

	normalizedListen, err := normalizeProxyObserverListenAddr(listenAddr)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: invalid --listen: %v\n", err)
		return 2
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(timeoutText))
	if err != nil || timeout <= 0 {
		if err == nil {
			err = errors.New("duration must be greater than zero")
		}
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: invalid --timeout: %v\n", err)
		return 2
	}
	if err := ensureProxyObserverEnvFileExists(envFile); err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: environment file %q is unavailable: %v\n", envFile, err)
		return 1
	}

	if dependencies.random == nil {
		dependencies.random = cryptorand.Reader
	}
	if dependencies.listen == nil {
		dependencies.listen = net.Listen
	}
	if dependencies.shutdownTimeout <= 0 {
		dependencies.shutdownTimeout = 5 * time.Second
	}

	observedCIDR, err := observeProxySource(normalizedListen, timeout, stdout, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}

	confirmed, err := confirmProxyCIDR(bufio.NewReader(stdin), stdout, stderr, envFile, observedCIDR)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(stdout, "Rejected. The environment file was not changed.")
		return 0
	}

	lock, err := acquireEnvFileLock(envFile)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}
	defer lock.Close()

	original, err := captureEnvFileSnapshot(envFile)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}
	values, err := readEnvFile(envFile)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}
	values["TRUSTED_PROXY_CIDR"] = observedCIDR
	if err := writeEnvFileWithSnapshot(envFile, values, &original); err != nil {
		fmt.Fprintf(stderr, "velociportal: setup observe-proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated TRUSTED_PROXY_CIDR in %s to %s; all key/value entries were preserved in canonical format.\n", envFile, observedCIDR)
	fmt.Fprintln(stdout, "Next repository command: make doctor")
	fmt.Fprintf(stdout, "Direct-binary equivalent: velociportal doctor --env-file %s\n", quoteSetupCommandArgument(envFile))
	return 0
}

func normalizeProxyObserverListenAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("must be an explicit host:port: %w", err)
	}
	if portText != "0" {
		return normalizeListenAddr(raw)
	}

	validated, err := normalizeListenAddr(net.JoinHostPort(host, "1"))
	if err != nil {
		return "", err
	}
	validatedHost, _, err := net.SplitHostPort(validated)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(validatedHost, "0"), nil
}

func newProxyObservationPath(random io.Reader) (string, error) {
	if random == nil {
		return "", errors.New("generate observation path: nil random source")
	}
	token := make([]byte, 24)
	if _, err := io.ReadFull(random, token); err != nil {
		return "", fmt.Errorf("generate observation path: %w", err)
	}
	return proxyObservationPathPrefix + base64.RawURLEncoding.EncodeToString(token), nil
}

func observeProxySource(listenAddr string, timeout time.Duration, stdout io.Writer, dependencies proxyObserverDependencies) (string, error) {
	path, err := newProxyObservationPath(dependencies.random)
	if err != nil {
		return "", err
	}
	listener, err := dependencies.listen("tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	observed := make(chan string, 1)
	handler := &proxyObservationHandler{path: path, observed: observed}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	serveErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()

	fmt.Fprintln(stdout, "Temporary proxy observer started. It has no upstream API credentials or identity trust configuration.")
	fmt.Fprintf(stdout, "Listening on: %s\n", listener.Addr().String())
	fmt.Fprintf(stdout, "One-time path: %s\n", path)
	fmt.Fprintln(stdout, "Send one GET request through the final identity-aware proxy to this path.")
	fmt.Fprintln(stdout, "Forwarded, X-Forwarded-For, X-Real-IP, and Tailscale-User-* headers are ignored.")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var observedCIDR string
	var resultErr error
	select {
	case observedCIDR = <-observed:
		fmt.Fprintf(stdout, "Observed immediate connection source: %s\n", observedCIDR)
	case err := <-serveErrors:
		resultErr = fmt.Errorf("observer server failed: %w", err)
	case <-ctx.Done():
		resultErr = fmt.Errorf("proxy observation timed out after %s", timeout)
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), dependencies.shutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = listener.Close()
		if resultErr == nil {
			resultErr = fmt.Errorf("shut down observer: %w", err)
		}
	}
	if resultErr != nil {
		return "", resultErr
	}
	return observedCIDR, nil
}

func (handler *proxyObservationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setProxyObserverNoStore(response.Header())
	if request.URL.Path != handler.path {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cidr, err := exactCIDRFromRemoteAddr(request.RemoteAddr)
	if err != nil {
		http.Error(response, "invalid connection source", http.StatusBadRequest)
		return
	}
	if !handler.claimed.CompareAndSwap(false, true) {
		http.Error(response, "observation path already used", http.StatusGone)
		return
	}

	handler.observed <- cidr
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, "Proxy source observed. Return to the setup terminal to review it.\n")
}

func setProxyObserverNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func exactCIDRFromRemoteAddr(remoteAddr string) (string, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return "", fmt.Errorf("parse RemoteAddr: %w", err)
	}
	if percent := strings.LastIndexByte(host, '%'); percent >= 0 {
		host = host[:percent]
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("parse RemoteAddr IP: %w", err)
	}
	address = address.Unmap()
	bits := 128
	if address.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(address, bits).String(), nil
}

func confirmProxyCIDR(reader *bufio.Reader, stdout, stderr io.Writer, envFile, observedCIDR string) (bool, error) {
	for {
		fmt.Fprintf(stdout, "Update only TRUSTED_PROXY_CIDR in %s to %s? [y/N]: ", envFile, observedCIDR)
		line, err := readSetupLine(reader)
		if err != nil {
			return false, fmt.Errorf("read confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			fmt.Fprintln(stderr, "Enter yes or no.")
		}
	}
}

func ensureProxyObserverEnvFileExists(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("environment file %q is not a regular file", path)
	}
	return nil
}
