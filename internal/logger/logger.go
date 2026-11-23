package logger

import (
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"projectlens/internal/config"
)

func New(cfg config.LogConfig, out io.Writer) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(out, options))
	}
	return slog.New(slog.NewTextHandler(out, options))
}

const redacted = "[REDACTED]"

func redactAttr(groups []string, attr slog.Attr) slog.Attr {
	if sensitiveKey(attr.Key) || slices.ContainsFunc(groups, sensitiveKey) {
		return slog.String(attr.Key, redacted)
	}
	return attr
}

var keySeparators = strings.NewReplacer("-", "_", ".", "_")

func sensitiveKey(key string) bool {
	key = keySeparators.Replace(strings.ToLower(key))
	switch key {
	case "auth", "auth_enabled", "auth_token_count":
		return false
	}
	key = "_" + key + "_"
	for _, secret := range []string{"authorization", "token", "bearer", "password", "secret", "api_key", "apikey", "cookie", "dsn", "private_key"} {
		if strings.Contains(key, "_"+secret+"_") {
			return true
		}
	}
	return false
}

// RedactDSN returns the target of a PostgreSQL URL with only its host and path.
// Keyword DSNs and malformed URLs are masked in full.
func RedactDSN(raw string) string {
	endpoint, err := url.Parse(raw)
	if err != nil || (endpoint.Scheme != "postgres" && endpoint.Scheme != "postgresql") || endpoint.Hostname() == "" {
		return redacted
	}
	target := url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host, Path: endpoint.Path}
	return target.String()
}
