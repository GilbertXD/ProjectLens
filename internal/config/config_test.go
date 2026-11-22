package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != (AppConfig{Name: "ProjectLens", Env: "development", Version: "0.1.0"}) ||
		cfg.Server.Port != 8080 || cfg.Server.ReadTimeout != 5*time.Second ||
		cfg.Log != (LogConfig{Level: "info", Format: "text"}) || len(cfg.CORS.AllowedOrigins) != 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadYAML(t *testing.T) {
	path := writeConfig(t, `
app:
  name: Custom Lens
  env: staging
  version: "2.1"
server:
  port: 9090
  idle_timeout: 12s
log:
  format: json
cors:
  allowed_origins: [https://app.example]
  allowed_methods: [GET, PATCH]
  allowed_headers: [Authorization]
`)
	cfg, err := load(path, env(map[string]string{"APP_CONFIG_FILE": "unused.yaml"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != (AppConfig{Name: "Custom Lens", Env: "staging", Version: "2.1"}) ||
		cfg.Server.Port != 9090 || cfg.Server.IdleTimeout != 12*time.Second ||
		cfg.Server.ReadTimeout != 5*time.Second || cfg.Log != (LogConfig{Level: "info", Format: "json"}) {
		t.Fatalf("unexpected merged config: %+v", cfg)
	}
	wantCORS := CORSConfig{
		AllowedOrigins: []string{"https://app.example"},
		AllowedMethods: []string{"GET", "PATCH"},
		AllowedHeaders: []string{"Authorization"},
	}
	if !reflect.DeepEqual(cfg.CORS, wantCORS) {
		t.Fatalf("CORS = %+v, want %+v", cfg.CORS, wantCORS)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	path := writeConfig(t, "server: {port: 8082}\ncors: {allowed_methods: [PATCH]}\n")
	cfg, err := load("", env(map[string]string{
		"APP_CONFIG_FILE":         path,
		"APP_NAME":                "Env Lens",
		"APP_ENV":                 "test",
		"APP_VERSION":             "0.3.0",
		"SERVER_HOST":             "127.0.0.1",
		"SERVER_PORT":             "8081",
		"SERVER_READ_TIMEOUT":     "1s",
		"SERVER_WRITE_TIMEOUT":    "2s",
		"SERVER_IDLE_TIMEOUT":     "3s",
		"SERVER_SHUTDOWN_TIMEOUT": "4s",
		"LOG_LEVEL":               "warn",
		"LOG_FORMAT":              "json",
		"CORS_ALLOWED_ORIGINS":    "https://one.example, https://two.example, ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != (AppConfig{Name: "Env Lens", Env: "test", Version: "0.3.0"}) ||
		cfg.Log != (LogConfig{Level: "warn", Format: "json"}) {
		t.Fatalf("unexpected app or log config: %+v", cfg)
	}
	wantServer := ServerConfig{
		Host: "127.0.0.1", Port: 8081,
		ReadTimeout: time.Second, WriteTimeout: 2 * time.Second,
		IdleTimeout: 3 * time.Second, ShutdownTimeout: 4 * time.Second,
	}
	if cfg.Server != wantServer {
		t.Fatalf("server = %+v, want %+v", cfg.Server, wantServer)
	}
	if !reflect.DeepEqual(cfg.CORS.AllowedOrigins, []string{"https://one.example", "https://two.example"}) ||
		!reflect.DeepEqual(cfg.CORS.AllowedMethods, []string{"PATCH"}) {
		t.Fatalf("unexpected CORS config: %+v", cfg.CORS)
	}
}

func TestLoadInvalidFiles(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.yaml")
		_, err := load(path, env(nil))
		if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
			t.Fatalf("error = %v, want missing config path", err)
		}
	})

	for _, tc := range []struct{ name, yaml, want string }{
		{"malformed", "app: [\n", "decode config file"},
		{"unknown", "server: {prt: 8080}\n", "field prt not found"},
		{"multiple documents", "app: {name: Lens}\n---\nserver: {port: 8080}\n", "single YAML document"},
		{"invalid duration", "server: {read_timeout: someday}\n", "time.Duration"},
		{"invalid port", "server: {port: 65536}\n", "between 1 and 65535"},
		{"fractional port", "server: {port: 8080.5}\n", "server.port must be an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(writeConfig(t, tc.yaml), env(nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadInvalidOverrides(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"APP_NAME", " ", "must not be empty"},
		{"APP_ENV", "qa", "development, test, staging, or production"},
		{"LOG_LEVEL", "verbose", "debug, info, warn, or error"},
		{"LOG_FORMAT", "xml", "text or json"},
		{"SERVER_PORT", "0", "between 1 and 65535"},
		{"SERVER_PORT", "abc", "must be an integer"},
		{"SERVER_READ_TIMEOUT", "5", "must be a duration"},
		{"SERVER_WRITE_TIMEOUT", " ", "must be a duration"},
		{"SERVER_IDLE_TIMEOUT", "0s", "must be positive"},
		{"SERVER_SHUTDOWN_TIMEOUT", "-1s", "must be positive"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			_, err := load("", env(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s and %q", err, tc.key, tc.want)
			}
		})
	}
}

func TestDatabaseConfiguration(t *testing.T) {
	path := writeConfig(t, "database:\n  max_conns: 12\n  connect_timeout: 8s\n")
	yamlCfg, err := load(path, env(nil))
	if err != nil || yamlCfg.Database.MaxConns != 12 || yamlCfg.Database.ConnectTimeout != 8*time.Second ||
		yamlCfg.Database.MinConns != 2 || !yamlCfg.Database.AutoMigrate {
		t.Fatalf("database YAML = %+v, error = %v", yamlCfg.Database, err)
	}
	env := map[string]string{
		"DATABASE_URL": "postgres://db/test",
		"DB_MIN_CONNS": "0", "DB_MAX_CONNS": "15",
		"DB_MAX_CONN_LIFETIME": "40m", "DB_MAX_CONN_IDLE_TIME": "4m",
		"DB_HEALTH_CHECK_PERIOD": "10s", "DB_CONNECT_TIMEOUT": "9s", "DB_AUTO_MIGRATE": "false",
	}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	cfg, err := load(path, lookup)
	want := DatabaseConfig{
		DSN: env["DATABASE_URL"], MaxConns: 15, MinConns: 0, MaxConnLifetime: 40 * time.Minute,
		MaxConnIdleTime: 4 * time.Minute, HealthCheckPeriod: 10 * time.Second, ConnectTimeout: 9 * time.Second,
	}
	if err != nil || cfg.Database != want {
		t.Fatalf("database = %+v, error = %v, want %+v", cfg.Database, err, want)
	}
	for _, value := range []string{"1.5", `"2"`, "2147483648", "0"} {
		if err := os.WriteFile(path, []byte("database:\n  max_conns: "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := load(path, func(string) (string, bool) { return "", false }); err == nil {
			t.Errorf("expected max_conns %s to fail", value)
		}
	}
}
