package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const maxEnvFileLineSize = 1024 * 1024

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read env file %q: %w", path, err)
	}
	defer file.Close()

	values, err := parseEnvFile(file)
	if err != nil {
		return nil, fmt.Errorf("read env file %q: %w", path, err)
	}
	return values, nil
}

func parseEnvFile(reader io.Reader) (map[string]string, error) {
	if reader == nil {
		return nil, fmt.Errorf("parse env file: nil reader")
	}

	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxEnvFileLineSize+3)
	scanner.Split(splitEnvFileLines)

	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		switch {
		case strings.HasSuffix(line, "\r\n"):
			line = strings.TrimSuffix(line, "\r\n")
		case strings.HasSuffix(line, "\n"):
			line = strings.TrimSuffix(line, "\n")
		}
		if len(line) > maxEnvFileLineSize {
			return nil, fmt.Errorf("parse env file: line %d exceeds %d bytes", lineNumber, maxEnvFileLineSize)
		}
		if strings.ContainsAny(line, "\r\n\x00") {
			return nil, fmt.Errorf("parse env file: line %d contains a forbidden CR, LF, or NUL byte", lineNumber)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasPrefix(trimmed, "export") && len(trimmed) > len("export") && isEnvSpace(trimmed[len("export")]) {
			trimmed = strings.TrimLeft(trimmed[len("export"):], " \t")
		}

		equals := strings.IndexByte(trimmed, '=')
		if equals < 0 {
			return nil, fmt.Errorf("parse env file: line %d: expected KEY=VALUE", lineNumber)
		}

		key := strings.TrimSpace(trimmed[:equals])
		if err := validateEnvKey(key); err != nil {
			return nil, fmt.Errorf("parse env file: line %d: %w", lineNumber, err)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("parse env file: line %d: duplicate key %q", lineNumber, key)
		}

		rawValue := strings.TrimLeft(trimmed[equals+1:], " \t")
		value, err := parseEnvValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("parse env file: line %d, key %q: %w", lineNumber, key, err)
		}
		if err := validateEnvValue(value); err != nil {
			return nil, fmt.Errorf("parse env file: line %d, key %q: %w", lineNumber, key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse env file: %w", err)
	}

	return values, nil
}

func splitEnvFileLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
		return newline + 1, data[:newline+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parseEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	switch raw[0] {
	case '\'':
		closing := strings.IndexByte(raw[1:], '\'')
		if closing < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		closing++
		if err := validateQuotedRemainder(raw[closing+1:]); err != nil {
			return "", err
		}
		return raw[1:closing], nil
	case '"':
		closing := findDoubleQuoteEnd(raw)
		if closing < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		if err := validateQuotedRemainder(raw[closing+1:]); err != nil {
			return "", err
		}
		value, err := strconv.Unquote(raw[:closing+1])
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return value, nil
	default:
		return parseUnquotedEnvValue(raw), nil
	}
}

func findDoubleQuoteEnd(raw string) int {
	escaped := false
	for index := 1; index < len(raw); index++ {
		switch {
		case escaped:
			escaped = false
		case raw[index] == '\\':
			escaped = true
		case raw[index] == '"':
			return index
		}
	}
	return -1
}

func validateQuotedRemainder(remainder string) error {
	remainder = strings.TrimSpace(remainder)
	if remainder == "" || strings.HasPrefix(remainder, "#") {
		return nil
	}
	return fmt.Errorf("unexpected characters after quoted value")
}

func parseUnquotedEnvValue(raw string) string {
	comment := -1
	for index := 0; index < len(raw); index++ {
		if raw[index] != '#' {
			continue
		}
		if index == 0 || isEnvSpace(raw[index-1]) {
			comment = index
			break
		}
	}
	if comment >= 0 {
		raw = raw[:comment]
	}
	return strings.TrimSpace(raw)
}

func serializeEnvFile(values map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if err := validateEnvKey(key); err != nil {
			return nil, fmt.Errorf("serialize env file: %w", err)
		}
		if err := validateEnvValue(value); err != nil {
			return nil, fmt.Errorf("serialize env file: key %q: %w", key, err)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var output strings.Builder
	for _, key := range keys {
		output.WriteString(key)
		output.WriteByte('=')
		output.WriteString(strconv.Quote(values[key]))
		output.WriteByte('\n')
	}
	return []byte(output.String()), nil
}

type envFileSnapshot struct {
	exists   bool
	contents []byte
}

type envFileLock struct {
	directory *os.File
}

func acquireEnvFileLock(path string) (*envFileLock, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("lock env file: path must not be empty")
	}
	directoryPath := filepath.Dir(path)
	directory, err := os.Open(directoryPath)
	if err != nil {
		return nil, fmt.Errorf("lock env file %q: open directory: %w", path, err)
	}
	if err := syscall.Flock(int(directory.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = directory.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fmt.Errorf("environment file directory %q is being updated by another Velociportal command", directoryPath)
		}
		return nil, fmt.Errorf("lock env file %q: %w", path, err)
	}
	return &envFileLock{directory: directory}, nil
}

func (lock *envFileLock) Close() error {
	if lock == nil || lock.directory == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.directory.Fd()), syscall.LOCK_UN)
	closeErr := lock.directory.Close()
	lock.directory = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock env file directory: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close env file directory lock: %w", closeErr)
	}
	return nil
}

func captureEnvFileSnapshot(path string) (envFileSnapshot, error) {
	contents, err := os.ReadFile(path)
	if err == nil {
		return envFileSnapshot{exists: true, contents: contents}, nil
	}
	if os.IsNotExist(err) {
		return envFileSnapshot{}, nil
	}
	return envFileSnapshot{}, fmt.Errorf("snapshot env file %q: %w", path, err)
}

func envFileMatchesSnapshot(path string, expected envFileSnapshot) error {
	current, err := captureEnvFileSnapshot(path)
	if err != nil {
		return err
	}
	if current.exists != expected.exists || !bytes.Equal(current.contents, expected.contents) {
		return fmt.Errorf("environment file %q changed while the command was running; no update was applied", path)
	}
	return nil
}

func writeEnvFile(path string, values map[string]string) error {
	return writeEnvFileWithSnapshot(path, values, nil)
}

func writeEnvFileWithSnapshot(path string, values map[string]string, expected *envFileSnapshot) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("write env file: path must not be empty")
	}

	contents, err := serializeEnvFile(values)
	if err != nil {
		return fmt.Errorf("write env file %q: %w", path, err)
	}

	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("write env file %q: create temporary file: %w", path, err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write env file %q: set permissions: %w", path, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write env file %q: write: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write env file %q: sync: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write env file %q: close: %w", path, err)
	}
	if expected != nil {
		if err := envFileMatchesSnapshot(path, *expected); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("write env file %q: replace: %w", path, err)
	}

	keepTemporary = true
	return nil
}

func mapConfigLookup(values map[string]string) configLookup {
	return func(key string) (string, bool, error) {
		value, ok := values[key]
		return value, ok, nil
	}
}

func validateEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("environment key must not be empty")
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		if index == 0 {
			if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return fmt.Errorf("invalid environment key %q", key)
			}
			continue
		}
		if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return fmt.Errorf("invalid environment key %q", key)
		}
	}
	return nil
}

func validateEnvValue(value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("environment value must not contain CR, LF, or NUL")
	}
	return nil
}

func isEnvSpace(character byte) bool {
	return character == ' ' || character == '\t'
}
