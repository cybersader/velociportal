package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const setupServiceEditorUsage = `Usage:
  velociportal setup service-editor --env-file FILE --editors JSON --origin ORIGIN
  velociportal setup service-editor --env-file FILE --disable

Configure optional exact-login shared presentation editing. SERVICE_METADATA_FILE
must already be set. This command only updates the environment file; it does not
create, migrate, lock or probe metadata storage. Provision and privately review a
dedicated runtime-owned directory separately before enabling the server. Disabling
editing removes both settings, never metadata or icons. v3 needs a compatible binary.
`

func runSetupServiceEditorCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, setupServiceEditorUsage)
		return 0
	}
	flags := flag.NewFlagSet("service-editor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var path, editors, origin string
	var disable bool
	flags.StringVar(&path, "env-file", "", "existing environment file")
	flags.StringVar(&editors, "editors", "", "exact full-login JSON array")
	flags.StringVar(&origin, "origin", "", "canonical external HTTP(S) origin")
	flags.BoolVar(&disable, "disable", false, "disable editing without changing metadata")
	if flags.Parse(args) != nil || flags.NArg() != 0 || path == "" || (disable && (editors != "" || origin != "")) || (!disable && (editors == "" || origin == "")) {
		fmt.Fprint(stderr, setupServiceEditorUsage)
		return 2
	}
	if err := configureSetupServiceEditor(path, editors, origin, disable); err != nil {
		// Values and paths are deliberately omitted from this coarse diagnostic.
		fmt.Fprintln(stderr, "velociportal: service editor configuration could not be saved; check configuration and environment-file access")
		return 1
	}
	fmt.Fprintln(stdout, "Service editor settings saved. Metadata was not changed or provisioned; verify storage in the runtime context before restarting.")
	return 0
}

func configureSetupServiceEditor(path, editors, origin string, disable bool) error {
	lock, err := acquireEnvFileLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	snapshot, err := captureEnvFileSnapshot(path)
	if err != nil {
		return err
	}
	values, exists, err := readSetupValues(path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("existing environment file required")
	}
	if disable {
		delete(values, "PORTAL_EDITORS")
		delete(values, "PORTAL_PUBLIC_ORIGIN")
	} else {
		values["PORTAL_EDITORS"], values["PORTAL_PUBLIC_ORIGIN"] = editors, origin
		if _, err := loadServiceMetadataEditorConfig(mapConfigLookup(values), strings.TrimSpace(values["SERVICE_METADATA_FILE"])); err != nil {
			return err
		}
	}
	return writeEnvFileWithSnapshot(path, values, &snapshot)
}
