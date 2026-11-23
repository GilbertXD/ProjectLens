package main

import (
	"bytes"
	"context"
	"errors"
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

func TestRunHTTPAndWorkerModes(t *testing.T) {
	for _, mode := range []string{"embedded", "external", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			drained := make(chan struct{})
			want := errors.New("HTTP listener failed")
			err := runHTTPAndWorker(t.Context(), mode, func(context.Context) error { return want },
				func(ctx context.Context) {
					<-ctx.Done()
					close(drained)
				})
			if !errors.Is(err, want) {
				t.Fatalf("err = %v", err)
			}
			select {
			case <-drained:
				if mode != "embedded" {
					t.Fatal("started worker in API-only mode")
				}
			default:
				if mode == "embedded" {
					t.Fatal("returned before worker drained")
				}
			}
		})
	}
}

func TestRunProductionStartupRedactsDatabaseCredentials(t *testing.T) {
	t.Setenv("APP_CONFIG_FILE", "../../configs/config.yaml")
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_ENABLED", "false")
	t.Setenv("AUTH_BEARER_TOKENS", "startup-token-fixture")
	t.Setenv("DATABASE_URL", "postgres://user:startup-password@db:invalid/projectlens?password=startup-secret")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("LOG_LEVEL", "info")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want invalid database failure", code)
	}
	log := stdout.String()
	for _, want := range []string{"server starting", "server stopped", "auth_enabled", "auth_token_count", "WARN", "API authentication is disabled in production"} {
		if !strings.Contains(log, want) {
			t.Errorf("logs missing %q", want)
		}
	}
	if strings.Contains(log+stderr.String(), "startup-") {
		t.Fatal("startup diagnostics contain credentials")
	}
}
