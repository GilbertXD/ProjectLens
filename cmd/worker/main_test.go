package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerFlags(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		status int
		output string
	}{
		{[]string{"-help"}, 0, "Usage of worker"},
		{[]string{"unexpected"}, 2, "unexpected argument"},
		{[]string{"-config", filepath.Join(t.TempDir(), "missing.yaml")}, 1, "open config file"},
	} {
		var stderr bytes.Buffer
		if code := run(tc.args, io.Discard, &stderr); code != tc.status || !strings.Contains(stderr.String(), tc.output) {
			t.Fatalf("run(%v) = %d, %s", tc.args, code, &stderr)
		}
	}
}

func TestWorkerInvalidConfiguration(t *testing.T) {
	t.Setenv("WORKER_CONCURRENCY", "0")
	var stderr bytes.Buffer
	if code := run([]string{"-config", "../../configs/config.yaml"}, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "WORKER_CONCURRENCY") {
		t.Fatalf("exit = %d, stderr = %s", code, &stderr)
	}
}
