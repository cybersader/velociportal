package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvFileSyntaxAndLineEndings(t *testing.T) {
	input := strings.Join([]string{
		"# full-line comment",
		"",
		"export EXPORTED = unquoted value",
		"INLINE=value # trailing comment",
		"HASH=value#literal",
		"EMPTY=",
		"SINGLE=' $HOME # literal '",
		`DOUBLE="quote\" slash\\ tab\t dollar=$HOME" # comment`,
		"URL=https://example.com/path#fragment",
	}, "\r\n") + "\r\n"

	got, err := parseEnvFile(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v", err)
	}
	want := map[string]string{
		"EXPORTED": "unquoted value",
		"INLINE":   "value",
		"HASH":     "value#literal",
		"EMPTY":    "",
		"SINGLE":   " $HOME # literal ",
		"DOUBLE":   "quote\" slash\\ tab\t dollar=$HOME",
		"URL":      "https://example.com/path#fragment",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileDoesNotInterpolate(t *testing.T) {
	t.Setenv("SECRET_FROM_ENV", "expanded")
	got, err := parseEnvFile(strings.NewReader("A=$SECRET_FROM_ENV\nB=${SECRET_FROM_ENV}\nC=prefix-$SECRET_FROM_ENV\n"))
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v", err)
	}
	want := map[string]string{
		"A": "$SECRET_FROM_ENV",
		"B": "${SECRET_FROM_ENV}",
		"C": "prefix-$SECRET_FROM_ENV",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEnvFile() = %#v, want literals %#v", got, want)
	}
}

func TestParseEnvFileRejectsDuplicates(t *testing.T) {
	_, err := parseEnvFile(strings.NewReader("KEY=first\nexport KEY=second\n"))
	if err == nil || !strings.Contains(err.Error(), `duplicate key "KEY"`) {
		t.Fatalf("parseEnvFile() error = %v", err)
	}
}

func TestParseEnvFileRejectsMalformedInput(t *testing.T) {
	tests := map[string]string{
		"missing equals":          "KEY\n",
		"empty key":               "=value\n",
		"invalid key":             "BAD-KEY=value\n",
		"key starts with digit":   "1KEY=value\n",
		"unterminated single":     "KEY='value\n",
		"unterminated double":     "KEY=\"value\n",
		"trailing quoted content": "KEY=\"value\" surprise\n",
		"invalid double escape":   "KEY=\"bad\\q\"\n",
		"quoted newline escape":   "KEY=\"bad\\nvalue\"\n",
		"NUL":                     "KEY=bad\x00value\n",
		"embedded CR":             "KEY=bad\rvalue\n",
		"terminal lone CR":        "KEY=value\r",
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEnvFile(strings.NewReader(input)); err == nil {
				t.Fatalf("parseEnvFile(%q) error = nil", input)
			}
		})
	}
}

func TestParseEnvFileRejectsOversizedLine(t *testing.T) {
	input := "KEY=" + strings.Repeat("x", maxEnvFileLineSize+1)
	if _, err := parseEnvFile(strings.NewReader(input)); err == nil {
		t.Fatal("parseEnvFile() accepted oversized line")
	}
}

func TestSerializeEnvFileIsCanonicalAndDeterministic(t *testing.T) {
	values := map[string]string{
		"Z_LAST":  "two words",
		"A_FIRST": "value#with=$pecial\\characters\" and 'quotes'",
		"EMPTY":   "",
	}

	first, err := serializeEnvFile(values)
	if err != nil {
		t.Fatalf("serializeEnvFile() error = %v", err)
	}
	second, err := serializeEnvFile(values)
	if err != nil {
		t.Fatalf("serializeEnvFile() second error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("serialization is not deterministic:\n%s\n%s", first, second)
	}

	want := "A_FIRST=\"value#with=$pecial\\\\characters\\\" and 'quotes'\"\nEMPTY=\"\"\nZ_LAST=\"two words\"\n"
	if string(first) != want {
		t.Fatalf("serializeEnvFile() = %q, want %q", first, want)
	}
}

func TestEnvFileSpecialCharacterRoundTrip(t *testing.T) {
	want := map[string]string{
		"ASCII":       "plain",
		"BACKSLASH":   `C:\\path\\file`,
		"DOLLAR":      "$HOME and ${USER}",
		"EMPTY":       "",
		"EQUALS_HASH": "a=b # c#d",
		"QUOTES":      `double " and single '`,
		"TAB":         "left\tright",
		"UNICODE":     "héllo 世界",
	}

	serialized, err := serializeEnvFile(want)
	if err != nil {
		t.Fatalf("serializeEnvFile() error = %v", err)
	}
	got, err := parseEnvFile(bytes.NewReader(serialized))
	if err != nil {
		t.Fatalf("parseEnvFile(serialized) error = %v\ncontents:\n%s", err, serialized)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestSerializeEnvFileRejectsUnsafeData(t *testing.T) {
	tests := []map[string]string{
		{"BAD-KEY": "value"},
		{"KEY": "line1\nline2"},
		{"KEY": "line1\rline2"},
		{"KEY": "before\x00after"},
	}
	for _, values := range tests {
		if _, err := serializeEnvFile(values); err == nil {
			t.Fatalf("serializeEnvFile(%#v) error = nil", values)
		}
	}
}

func TestWriteEnvFileAtomicModeAndUnknownKeyPreservation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "velociportal.env")

	initial := map[string]string{
		"HEADSCALE_URL":          "https://headscale.example.com",
		"UNKNOWN_FUTURE_SETTING": "keep me",
	}
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile(initial) error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}

	parsed, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	parsed["HEADSCALE_URL"] = "https://new-headscale.example.com"
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := writeEnvFile(path, parsed); err != nil {
		t.Fatalf("writeEnvFile(replacement) error = %v", err)
	}

	reloaded, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile(replacement) error = %v", err)
	}
	if reloaded["UNKNOWN_FUTURE_SETTING"] != "keep me" {
		t.Fatalf("unknown key was not preserved: %#v", reloaded)
	}
	if reloaded["HEADSCALE_URL"] != "https://new-headscale.example.com" {
		t.Fatalf("known key was not updated: %#v", reloaded)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(replacement) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("replacement mode = %o, want 600", got)
	}

	leftovers, err := filepath.Glob(filepath.Join(directory, ".velociportal.env.tmp-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestWriteEnvFileValidatesBeforeReplacing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	original := []byte("ORIGINAL=value\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := writeEnvFile(path, map[string]string{"KEY": "bad\nvalue"})
	if err == nil {
		t.Fatal("writeEnvFile() error = nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing file changed after validation error: %q", got)
	}
}

func TestAcquireEnvFileLockSerializesCooperatingWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	first, err := acquireEnvFileLock(path)
	if err != nil {
		t.Fatalf("acquireEnvFileLock(first) error = %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	if _, err := acquireEnvFileLock(path); err == nil || !strings.Contains(err.Error(), "another Velociportal command") {
		t.Fatalf("acquireEnvFileLock(second) error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}
	third, err := acquireEnvFileLock(path)
	if err != nil {
		t.Fatalf("acquireEnvFileLock(third) error = %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third.Close() error = %v", err)
	}
}

func TestWriteEnvFileWithSnapshotRejectsConcurrentChange(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := os.WriteFile(path, []byte("KEY=original\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	snapshot, err := captureEnvFileSnapshot(path)
	if err != nil {
		t.Fatalf("captureEnvFileSnapshot() error = %v", err)
	}
	concurrent := []byte("KEY=rotated\n")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatalf("concurrent WriteFile() error = %v", err)
	}

	err = writeEnvFileWithSnapshot(path, map[string]string{"KEY": "wizard"}, &snapshot)
	if err == nil || !strings.Contains(err.Error(), "changed while the command was running") {
		t.Fatalf("writeEnvFileWithSnapshot() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent contents were overwritten: %q", got)
	}
}
