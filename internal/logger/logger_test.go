package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"projectlens/internal/config"
)

func TestRedactAttributes(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			log := New(config.LogConfig{Level: "info", Format: format}, &out)
			attrs := []any{"auth_enabled", true, "auth_token_count", 2, "auth", "authenticated"}
			for _, key := range []string{"Authorization", "TOKEN", "bearer", "password", "secret", "api_key", "APIKey", "cookie", "dsn", "private_key", "github_token", "redis.password", "Set-Cookie"} {
				attrs = append(attrs, slog.Group("nested", slog.String(key, "secret-fixture")))
			}
			log.Info("startup", append(attrs, slog.Group("authorization", slog.String("value", "secret-fixture")))...)
			record := out.String()
			if strings.Contains(record, "secret-fixture") || strings.Count(record, redacted) != 14 {
				t.Fatal("credential attributes were not redacted")
			}
			for _, field := range []string{"auth_enabled", "auth_token_count", "authenticated"} {
				if !strings.Contains(record, field) {
					t.Errorf("safe metadata %q was removed", field)
				}
			}
		})
	}
}

func TestRedactDSN(t *testing.T) {
	for raw, want := range map[string]string{
		"postgres://alice:p%40ss@db:5432/projectlens?password=hunter2&sslmode=require": "postgres://db:5432/projectlens",
		"postgresql://alice@db/project%20lens#secret":                                  "postgresql://db/project%20lens",
		"host=db user=alice password=hunter2":                                          redacted,
		"postgres://user:password@":                                                    redacted,
		"%":                                                                            redacted,
	} {
		if got := RedactDSN(raw); got != want {
			t.Errorf("target = %q, want %q", got, want)
		}
	}
}
