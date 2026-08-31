package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const cliUsage = `Usage:
  velociportal                         Start the server using process environment
  velociportal serve [options]         Start the server
  velociportal setup [options]         Create or update local configuration
  velociportal setup observe-proxy     Observe the exact trusted proxy source
  velociportal doctor [options]        Run configuration and upstream diagnostics
  velociportal validate [options]      Build an explainable deployment-validation report
  velociportal suggest-hostnames ...   Propose private wildcard hostname metadata
  velociportal healthcheck [options]   Probe the local health endpoint
  velociportal help [command]          Show help

Commands:
  serve        Start the Velociportal server
  setup        Guide local configuration and trusted-proxy observation
  doctor       Check configuration, upstreams, joins, and identity previews
  validate           Compare labeled identities and explain supported join evidence
  suggest-hostnames  Privately propose metadata for wildcard-only NPM services
  healthcheck        Probe /healthz without loading application credentials
  help         Show help

Run "velociportal help <command>" for command options.
`

const serveUsage = `Usage:
  velociportal serve [--env-file FILE] [--listen ADDR]

Options:
  --env-file FILE  Load configuration only from FILE instead of process environment
  --listen ADDR    Override LISTEN_ADDR without changing the selected file
  -h, --help       Show this help
`

type cliCommands struct {
	serve            func(configLookup) error
	setup            func([]string, io.Reader, io.Writer, io.Writer) int
	doctor           func([]string, io.Writer, io.Writer) int
	validate         func([]string, io.Writer, io.Writer) int
	suggestHostnames func([]string, io.Reader, io.Writer, io.Writer) int
	healthcheck      func([]string, io.Writer, io.Writer) int
}

func defaultCLICommands() cliCommands {
	return cliCommands{
		serve:            runWithLookup,
		setup:            runSetupCommand,
		doctor:           runDoctorCommand,
		validate:         runValidationCommand,
		suggestHostnames: runHostnameSuggestionsCommand,
		healthcheck:      runHealthcheckCommand,
	}
}

func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runCLIWithCommands(args, stdin, stdout, stderr, defaultCLICommands())
}

func runCLIWithCommands(args []string, stdin io.Reader, stdout, stderr io.Writer, commands cliCommands) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 0 {
		return executeServe(processConfigLookup, stderr, commands.serve)
	}

	switch args[0] {
	case "help", "-h", "--help":
		return executeHelp(args, stdout, stderr)
	case "serve":
		return executeServeCommand(args[1:], stdout, stderr, commands.serve)
	case "setup":
		if commands.setup == nil {
			return unavailableCLICommand("setup", stderr)
		}
		return commands.setup(args[1:], stdin, stdout, stderr)
	case "doctor":
		if commands.doctor == nil {
			return unavailableCLICommand("doctor", stderr)
		}
		return commands.doctor(args[1:], stdout, stderr)
	case "validate":
		if commands.validate == nil {
			return unavailableCLICommand("validate", stderr)
		}
		return commands.validate(args[1:], stdout, stderr)
	case "suggest-hostnames":
		if commands.suggestHostnames == nil {
			return unavailableCLICommand("suggest-hostnames", stderr)
		}
		return commands.suggestHostnames(args[1:], stdin, stdout, stderr)
	case "healthcheck":
		if commands.healthcheck == nil {
			return unavailableCLICommand("healthcheck", stderr)
		}
		return commands.healthcheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "velociportal: unknown command %q\n\n%s", args[0], cliUsage)
		return 2
	}
}

func unavailableCLICommand(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "velociportal: %s: command handler is unavailable\n", name)
	return 1
}

func executeHelp(args []string, stdout, stderr io.Writer) int {
	if args[0] == "-h" || args[0] == "--help" {
		if len(args) != 1 {
			fmt.Fprintf(stderr, "velociportal: help does not accept arguments after %s\n\n%s", args[0], cliUsage)
			return 2
		}
		fmt.Fprint(stdout, cliUsage)
		return 0
	}

	if len(args) == 1 {
		fmt.Fprint(stdout, cliUsage)
		return 0
	}
	if len(args) != 2 {
		fmt.Fprintf(stderr, "velociportal: usage: velociportal help [command]\n\n%s", cliUsage)
		return 2
	}

	var usage string
	switch args[1] {
	case "serve":
		usage = serveUsage
	case "setup":
		usage = setupUsage
	case "doctor":
		usage = doctorUsage
	case "validate":
		usage = validationUsage
	case "suggest-hostnames":
		usage = hostnameSuggestionsUsage
	case "healthcheck":
		usage = healthcheckUsage
	case "help":
		usage = "Usage:\n  velociportal help [command]\n"
	default:
		fmt.Fprintf(stderr, "velociportal: unknown help topic %q\n\n%s", args[1], cliUsage)
		return 2
	}
	fmt.Fprint(stdout, usage)
	return 0
}

func executeServeCommand(args []string, stdout, stderr io.Writer, serve func(configLookup) error) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, serveUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: serve: %s cannot be combined with other arguments\n\n%s", arg, serveUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var envFile string
	var listenOverride string
	flags.Func("env-file", "load configuration from file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.Func("listen", "override listen address", func(value string) error {
		normalized, err := normalizeListenAddr(value)
		if err != nil {
			return err
		}
		listenOverride = normalized
		return nil
	})
	flags.Usage = func() {}

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: serve: %v\n\n%s", err, serveUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: serve does not accept positional arguments\n\n%s", serveUsage)
		return 2
	}

	lookup := configLookup(processConfigLookup)
	if envFile != "" {
		values, err := readEnvFile(envFile)
		if err != nil {
			fmt.Fprintf(stderr, "velociportal: serve: %v\n", err)
			return 1
		}
		lookup = mapConfigLookup(values)
	}
	if listenOverride != "" {
		baseLookup := lookup
		lookup = func(key string) (string, bool, error) {
			if key == "LISTEN_ADDR" {
				return listenOverride, true, nil
			}
			return baseLookup(key)
		}
	}

	return executeServe(lookup, stderr, serve)
}

func executeServe(lookup configLookup, stderr io.Writer, serve func(configLookup) error) int {
	if serve == nil {
		fmt.Fprintln(stderr, "velociportal: serve: command handler is unavailable")
		return 1
	}
	if err := serve(lookup); err != nil {
		fmt.Fprintf(stderr, "velociportal: %v\n", err)
		return 1
	}
	return 0
}
