package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMissingConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	t.Setenv("APP_CONFIG_FILE", "../../configs/config.yaml")
	var stderr bytes.Buffer
	if code := run([]string{"-config", path}, io.Discard, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1, stderr = %s", code, &stderr)
	}
	if message := stderr.String(); !strings.Contains(message, "open config file") || !strings.Contains(message, path) {
		t.Fatalf("stderr = %q, want missing config path", message)
	}
}

func TestRunInvalidEnvironment(t *testing.T) {
	t.Setenv("APP_CONFIG_FILE", "../../configs/config.yaml")
	t.Setenv("SERVER_PORT", "not-a-port")
	var stderr bytes.Buffer
	if code := run(nil, io.Discard, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1, stderr = %s", code, &stderr)
	}
	if message := stderr.String(); !strings.Contains(message, "SERVER_PORT must be an integer") {
		t.Fatalf("stderr = %q, want integer port error", message)
	}
}

func TestRunInvalidDatabaseConfiguration(t *testing.T) {
	t.Setenv("APP_CONFIG_FILE", "../../configs/config.yaml")
	t.Setenv("DATABASE_URL", "")
	var stderr bytes.Buffer
	if code := run(nil, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "DATABASE_URL") {
		t.Fatalf("exit = %d, stderr = %s, want database validation error", code, &stderr)
	}
}
