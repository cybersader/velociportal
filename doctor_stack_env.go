package main

import (
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
)

const (
	stackEnvDefaultSubnet  = "172.31.255.0/24"
	stackEnvDefaultGateway = "172.31.255.1"
)

var (
	stackEnvDigestRE        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stackEnvImageTagRE      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	stackEnvImageNamePartRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	stackEnvPortRE          = regexp.MustCompile(`^[0-9]+$`)
)

func runDoctorStackEnvChecks(writer io.Writer, path string, providerLookup, interpolationLookup configLookup) (string, bool) {
	values, err := readEnvFile(path)
	if err != nil {
		fmt.Fprintf(writer, "FAIL stack env: %s\n", sanitizeDoctorError(err, nil))
		return "", true
	}
	fmt.Fprintf(writer, "PASS stack env source: loaded %d declared value(s) from %q\n", len(values), path)
	if applyDoctorStackEnvInterpolationOverrides(writer, values, interpolationLookup) {
		return "", true
	}

	failed := doctorStackEnvImage(writer, values)
	if doctorStackEnvNetwork(writer, values) {
		failed = true
	}
	if doctorStackEnvTrustedProxy(writer, values, providerLookup) {
		failed = true
	}
	return values["VELOCIPORTAL_TRUSTED_PROXY_CIDR"], failed
}

func applyDoctorStackEnvInterpolationOverrides(writer io.Writer, values map[string]string, lookup configLookup) bool {
	if lookup == nil {
		return false
	}
	for _, key := range []string{
		"VELOCIPORTAL_IMAGE",
		"VELOCIPORTAL_SUBNET",
		"VELOCIPORTAL_GATEWAY",
		"VELOCIPORTAL_TRUSTED_PROXY_CIDR",
	} {
		value, present, err := lookup(key)
		if err != nil {
			fmt.Fprintf(writer, "FAIL stack env source: cannot read process-environment override for %s\n", key)
			return true
		}
		if !present {
			continue
		}
		values[key] = value
		fmt.Fprintf(writer, "WARN stack env source: process environment overrides %s during Compose interpolation\n", key)
	}
	return false
}

func doctorStackEnvConfigLookup(base configLookup, trustedProxyCIDR string) configLookup {
	return func(key string) (string, bool, error) {
		if key == "TRUSTED_PROXY_CIDR" {
			return trustedProxyCIDR, true, nil
		}
		return base(key)
	}
}

func doctorStackEnvImage(writer io.Writer, values map[string]string) bool {
	image, present := values["VELOCIPORTAL_IMAGE"]
	if !present || image == "" {
		fmt.Fprintln(writer, "FAIL stack env image: VELOCIPORTAL_IMAGE is not set; production Compose requires an explicit value")
		return true
	}
	if image != strings.TrimSpace(image) {
		fmt.Fprintln(writer, "FAIL stack env image: VELOCIPORTAL_IMAGE must not contain leading or trailing whitespace")
		return true
	}

	if base, digest, hasDigest := strings.Cut(image, "@"); hasDigest {
		algorithm, encoded, supported := strings.Cut(digest, ":")
		_, _, validBase := parseDockerImageNameAndTag(base)
		if !supported || algorithm != "sha256" || !stackEnvDigestRE.MatchString(encoded) || !validBase {
			fmt.Fprintln(writer, "FAIL stack env image: VELOCIPORTAL_IMAGE contains a malformed or unsupported digest pin")
			return true
		}
		fmt.Fprintln(writer, "PASS stack env image: VELOCIPORTAL_IMAGE is pinned to an immutable sha256 digest")
		return false
	}

	tag, hasTag, valid := parseDockerImageNameAndTag(image)
	if !valid {
		fmt.Fprintln(writer, "FAIL stack env image: VELOCIPORTAL_IMAGE must contain a valid lowercase Docker image name and optional tag")
		return true
	}
	if !hasTag {
		fmt.Fprintln(writer, "FAIL stack env image: VELOCIPORTAL_IMAGE has no explicit tag or digest; Docker would resolve the mutable latest tag")
		return true
	}
	if strings.EqualFold(tag, "latest") {
		fmt.Fprintln(writer, `FAIL stack env image: VELOCIPORTAL_IMAGE must not use the mutable "latest" tag`)
		return true
	}
	fmt.Fprintf(writer, "WARN stack env image: VELOCIPORTAL_IMAGE uses mutable tag %q; prefer an immutable @sha256 digest for production\n", tag)
	return false
}

func parseDockerImageNameAndTag(reference string) (string, bool, bool) {
	name := reference
	tag := ""
	hasTag := false
	lastSlash := strings.LastIndex(reference, "/")
	if lastColon := strings.LastIndex(reference, ":"); lastColon > lastSlash {
		name = reference[:lastColon]
		tag = reference[lastColon+1:]
		hasTag = true
		if !stackEnvImageTagRE.MatchString(tag) {
			return "", false, false
		}
	}
	if !validDockerImageName(name) {
		return "", false, false
	}
	return tag, hasTag, true
}

func validDockerImageName(name string) bool {
	if name == "" || len(name) > 255 || name != strings.ToLower(name) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) == 0 {
		return false
	}
	pathStart := 0
	if strings.Contains(parts[0], ":") {
		if len(parts) < 2 {
			return false
		}
		host, port, found := strings.Cut(parts[0], ":")
		if !found || !stackEnvImageNamePartRE.MatchString(host) || !stackEnvPortRE.MatchString(port) {
			return false
		}
		pathStart = 1
	}
	for _, part := range parts[pathStart:] {
		if !stackEnvImageNamePartRE.MatchString(part) {
			return false
		}
	}
	return true
}

func doctorStackEnvNetwork(writer io.Writer, values map[string]string) bool {
	subnetRaw, subnetSource := stackEnvValueOrDefault(values, "VELOCIPORTAL_SUBNET", stackEnvDefaultSubnet)
	gatewayRaw, gatewaySource := stackEnvValueOrDefault(values, "VELOCIPORTAL_GATEWAY", stackEnvDefaultGateway)

	_, subnet, err := net.ParseCIDR(subnetRaw)
	if err != nil {
		fmt.Fprintf(writer, "FAIL stack env network: VELOCIPORTAL_SUBNET %q is not a valid CIDR\n", subnetRaw)
		return true
	}
	gateway := net.ParseIP(gatewayRaw)
	if gateway == nil {
		fmt.Fprintf(writer, "FAIL stack env network: VELOCIPORTAL_GATEWAY %q is not a valid IP address\n", gatewayRaw)
		return true
	}
	if !subnet.Contains(gateway) {
		fmt.Fprintf(
			writer,
			"FAIL stack env network: VELOCIPORTAL_GATEWAY %s (%s) is not contained by VELOCIPORTAL_SUBNET %s (%s)\n",
			gateway.String(), gatewaySource, subnet.String(), subnetSource,
		)
		return true
	}
	fmt.Fprintf(
		writer,
		"PASS stack env network: VELOCIPORTAL_GATEWAY %s (%s) is contained by VELOCIPORTAL_SUBNET %s (%s)\n",
		gateway.String(), gatewaySource, subnet.String(), subnetSource,
	)
	return false
}

func stackEnvValueOrDefault(values map[string]string, key, fallback string) (string, string) {
	if value, present := values[key]; present && value != "" {
		return value, "explicit"
	}
	return fallback, "default"
}

func doctorStackEnvTrustedProxy(writer io.Writer, values map[string]string, lookup configLookup) bool {
	raw := values["VELOCIPORTAL_TRUSTED_PROXY_CIDR"]
	if raw == "" {
		fmt.Fprintln(writer, "FAIL stack env trusted proxy: VELOCIPORTAL_TRUSTED_PROXY_CIDR is not set; production Compose requires an explicit value")
		return true
	}
	address, network, err := net.ParseCIDR(raw)
	if err != nil {
		fmt.Fprintf(writer, "FAIL stack env trusted proxy: VELOCIPORTAL_TRUSTED_PROXY_CIDR %q is not a valid CIDR\n", raw)
		return true
	}
	reportDoctorSingleAddressCIDR(writer, "stack env trusted proxy", "VELOCIPORTAL_TRUSTED_PROXY_CIDR "+raw, network)

	gatewayRaw, _ := stackEnvValueOrDefault(values, "VELOCIPORTAL_GATEWAY", stackEnvDefaultGateway)
	if gateway := net.ParseIP(gatewayRaw); gateway != nil {
		if address.Equal(gateway) {
			fmt.Fprintf(writer, "PASS stack env trusted proxy: address matches VELOCIPORTAL_GATEWAY %s\n", gateway.String())
		} else {
			fmt.Fprintf(
				writer,
				"WARN stack env trusted proxy: address %s does not match VELOCIPORTAL_GATEWAY %s; confirm the actual bridge gateway seen by Tailscale Serve traffic\n",
				address.String(), gateway.String(),
			)
		}
	}

	if lookup != nil {
		if providerValue, present, lookupErr := lookup("TRUSTED_PROXY_CIDR"); lookupErr == nil && present && strings.TrimSpace(providerValue) != "" {
			fmt.Fprintln(writer, "WARN stack env trusted proxy: provider env TRUSTED_PROXY_CIDR is overridden in production by VELOCIPORTAL_TRUSTED_PROXY_CIDR from stack.env")
		}
	}
	return false
}
