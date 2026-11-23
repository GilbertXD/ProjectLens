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

func TestRepositoryConfig(t *testing.T) {
	cfg, err := load("", env(nil))
	if err != nil || cfg.Repository.AllowedLocalRoots == nil || len(cfg.Repository.AllowedLocalRoots) != 0 ||
		cfg.GitHub != (GitHubConfig{APIBaseURL: "https://api.github.com", Timeout: 10 * time.Second, UserAgent: "ProjectLens"}) {
		t.Fatal("unexpected repository configuration defaults", err)
	}
	path := writeConfig(t, `
repository:
  allowed_local_roots: [/yaml]
github:
  api_base_url: http://localhost:8000
  token: yaml-value
  timeout: 2s
  user_agent: YAML
`)
	cfg, err = load(path, env(nil))
	if err != nil || !reflect.DeepEqual(cfg.Repository.AllowedLocalRoots, []string{"/yaml"}) ||
		cfg.GitHub != (GitHubConfig{APIBaseURL: "http://localhost:8000", Token: "yaml-value", Timeout: 2 * time.Second, UserAgent: "YAML"}) {
		t.Fatal("unexpected repository YAML configuration", err)
	}
	cfg, err = load(path, env(map[string]string{
		"REPO_ALLOWED_LOCAL_ROOTS": "/one, /two", "GITHUB_API_BASE_URL": "https://example.com/api",
		"GITHUB_TOKEN": "env-value", "GITHUB_TIMEOUT": "3s", "GITHUB_USER_AGENT": "Environment",
	}))
	if err != nil || !reflect.DeepEqual(cfg.Repository.AllowedLocalRoots, []string{"/one", "/two"}) ||
		cfg.GitHub != (GitHubConfig{APIBaseURL: "https://example.com/api", Token: "env-value", Timeout: 3 * time.Second, UserAgent: "Environment"}) {
		t.Fatal("unexpected repository environment configuration", err)
	}
	cfg, err = load(path, env(map[string]string{"REPO_ALLOWED_LOCAL_ROOTS": ""}))
	if err != nil || len(cfg.Repository.AllowedLocalRoots) != 0 {
		t.Fatal("empty environment variable should clear configured roots", err)
	}
}

func TestInvalidRepositoryConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"REPO_ALLOWED_LOCAL_ROOTS", "relative"}, {"REPO_ALLOWED_LOCAL_ROOTS", "/absolute,"}, {"REPO_ALLOWED_LOCAL_ROOTS", "/x\x00"},
		{"GITHUB_API_BASE_URL", "ftp://example.com"}, {"GITHUB_API_BASE_URL", "https://"}, {"GITHUB_API_BASE_URL", "://bad"},
		{"GITHUB_TIMEOUT", "0s"}, {"GITHUB_TIMEOUT", "-5s"}, {"GITHUB_TIMEOUT", "invalid"}, {"GITHUB_USER_AGENT", " "},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			if _, err := load("", env(map[string]string{tc.key: tc.value})); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected validation error for %s, got %v", tc.key, err)
			}
		})
	}
	for _, yaml := range []string{
		"repository: {allowed_local_root: []}", "github: {time_out: 3s}", "repository: {allowed_local_roots: ['']}",
	} {
		if _, err := load(writeConfig(t, yaml), env(nil)); err == nil {
			t.Errorf("accepted invalid YAML %s", yaml)
		}
	}
}

func TestWorkerConfig(t *testing.T) {
	cfg, err := load("", env(nil))
	want := WorkerConfig{
		Mode: "embedded", Concurrency: 4, PollInterval: 500 * time.Millisecond,
		LeaseDuration: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		DefaultTimeout: 5 * time.Minute, DefaultMaxAttempts: 3, RetryBaseDelay: 2 * time.Second,
		RetryMaxDelay: time.Minute, ShutdownTimeout: 15 * time.Second,
	}
	if err != nil || cfg.Worker != want {
		t.Fatalf("defaults = %+v, err = %v", cfg.Worker, err)
	}
	cfg, err = load(writeConfig(t, "worker:\n  mode: disabled\n  id: yaml\n"), env(map[string]string{
		"WORKER_MODE": "external", "WORKER_ID": "node-1", "WORKER_CONCURRENCY": "2", "WORKER_POLL_INTERVAL": "3s",
		"WORKER_LEASE_DURATION": "12s", "WORKER_HEARTBEAT_INTERVAL": "4s", "WORKER_DEFAULT_TIMEOUT": "1h",
		"WORKER_DEFAULT_MAX_ATTEMPTS": "12", "WORKER_RETRY_BASE_DELAY": "3s", "WORKER_RETRY_MAX_DELAY": "4s",
		"WORKER_SHUTDOWN_TIMEOUT": "9s",
	}))
	want = WorkerConfig{Mode: "external", ID: "node-1", Concurrency: 2, PollInterval: 3 * time.Second,
		LeaseDuration: 12 * time.Second, HeartbeatInterval: 4 * time.Second, DefaultTimeout: time.Hour,
		DefaultMaxAttempts: 12, RetryBaseDelay: 3 * time.Second, RetryMaxDelay: 4 * time.Second, ShutdownTimeout: 9 * time.Second}
	if err != nil || cfg.Worker != want {
		t.Fatalf("overrides = %+v, err = %v", cfg.Worker, err)
	}
}

func TestWorkerInvalidConfig(t *testing.T) {
	for _, input := range []string{"concurrency: 2.5", "default_max_attempts: 3.0", "unknown: true"} {
		t.Run(input, func(t *testing.T) {
			if _, err := load(writeConfig(t, "worker:\n  "+input+"\n"), env(nil)); err == nil {
				t.Fatalf("accepted %q", input)
			}
		})
	}
	for key, value := range map[string]string{
		"WORKER_MODE": "other", "WORKER_CONCURRENCY": "0", "WORKER_POLL_INTERVAL": "0s",
		"WORKER_LEASE_DURATION": "-1s", "WORKER_HEARTBEAT_INTERVAL": "30s", "WORKER_DEFAULT_TIMEOUT": "999ms",
		"WORKER_DEFAULT_MAX_ATTEMPTS": "101", "WORKER_RETRY_BASE_DELAY": "0s", "WORKER_RETRY_MAX_DELAY": "1s",
		"WORKER_SHUTDOWN_TIMEOUT": "0s",
	} {
		t.Run(key, func(t *testing.T) {
			if _, err := load("", env(map[string]string{key: value})); err == nil {
				t.Fatalf("accepted %s=%s", key, value)
			}
		})
	}
}

func TestWorkerIDRejectsNUL(t *testing.T) {
	_, err := load("", env(map[string]string{"WORKER_ID": "node\x00one"}))
	if err == nil || !strings.Contains(err.Error(), "worker.id (WORKER_ID)") {
		t.Fatalf("expected worker ID validation error, got %v", err)
	}
}

func TestScannerConfig(t *testing.T) {
	cfg, err := load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scanner.CacheDir != "" || cfg.Scanner.GitTimeout != 2*time.Minute ||
		cfg.Scanner.MaxFileSize != 1048576 || cfg.Scanner.MaxTotalBytes != 52428800 ||
		cfg.Scanner.MaxFiles != 10000 || cfg.Scanner.MaxPathLength != 512 || cfg.Scanner.MaxPathDepth != 32 {
		t.Fatalf("unexpected scanner defaults: %+v", cfg.Scanner)
	}
	path := writeConfig(t, "scanner:\n  max_files: 17\n  exclude_patterns: ['*.tmp']\n")
	overrides := map[string]string{
		"SCANNER_CACHE_DIR": t.TempDir(), "SCANNER_GIT_TIMEOUT": "15s",
		"SCANNER_MAX_FILE_SIZE": "10737418240", "SCANNER_MAX_TOTAL_BYTES": "21474836480",
		"SCANNER_MAX_PATH_LENGTH": "64", "SCANNER_MAX_PATH_DEPTH": "8",
		"SCANNER_EXCLUDE_PATTERNS": " generated, *.log ",
	}
	cfg, err = load(path, env(overrides))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Scanner
	if s.CacheDir != overrides["SCANNER_CACHE_DIR"] || s.GitTimeout != 15*time.Second || s.MaxFileSize != 10737418240 ||
		s.MaxTotalBytes != 21474836480 || s.MaxFiles != 17 || s.MaxPathLength != 64 || s.MaxPathDepth != 8 ||
		strings.Join(s.ExcludePatterns, ",") != "generated,*.log" {
		t.Fatalf("unexpected scanner overrides: %+v", s)
	}

	for _, value := range []string{"max_file_size: 1.0", "max_total_bytes: '12'", "max_files: 0",
		"max_path_length: 1.5", "max_path_depth: -1", "git_timeout: 0s", "cache_dir: ./cache", "max_total_bytes: 2", "unknown: 1"} {
		path := writeConfig(t, "scanner:\n  "+value+"\n")
		if _, err := load(path, env(nil)); err == nil {
			t.Errorf("accepted scanner config %q", value)
		}
	}
	for name, value := range map[string]string{
		"SCANNER_MAX_FILES": "1.5", "SCANNER_MAX_TOTAL_BYTES": "9223372036854775808", "SCANNER_CACHE_DIR": "/tmp/\x00cache",
	} {
		if _, err := load("", env(map[string]string{name: value})); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("accepted %s=%q", name, value)
		}
	}
}

func TestScannerRejectsInvalidExcludePatterns(t *testing.T) {
	for _, pattern := range []string{"", " ", "a\x00b", "foo["} {
		cfg := DefaultScannerConfig()
		cfg.ExcludePatterns = []string{pattern}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SCANNER_EXCLUDE_PATTERNS") {
			t.Errorf("Validate pattern %q returned %v", pattern, err)
		}
	}
	for _, value := range []string{"generated,", "generated, ,*.log", "foo[", "a\x00b"} {
		_, err := load("", env(map[string]string{"SCANNER_EXCLUDE_PATTERNS": value}))
		if err == nil || !strings.Contains(err.Error(), "SCANNER_EXCLUDE_PATTERNS") {
			t.Errorf("loading SCANNER_EXCLUDE_PATTERNS=%q returned %v", value, err)
		}
	}
}

func TestSyncConfig(t *testing.T) {
	cfg, err := load("", env(nil))
	if err != nil || cfg.Sync != (SyncConfig{RetainSuperseded: 2, FailedRetention: 24 * time.Hour}) {
		t.Fatalf("default sync config: %+v, %v", cfg.Sync, err)
	}
	path := writeConfig(t, "sync: {retain_superseded: 5, failed_retention: 3h}\n")
	cfg, err = load(path, env(nil))
	if err != nil || cfg.Sync != (SyncConfig{RetainSuperseded: 5, FailedRetention: 3 * time.Hour}) {
		t.Fatalf("YAML sync config: %+v, %v", cfg.Sync, err)
	}
	cfg, err = load(path, env(map[string]string{"SYNC_RETAIN_SUPERSEDED": "0", "SYNC_FAILED_RETENTION": "1h"}))
	if err != nil || cfg.Sync != (SyncConfig{RetainSuperseded: 0, FailedRetention: time.Hour}) {
		t.Fatalf("environment sync config: %+v, %v", cfg.Sync, err)
	}
	for _, input := range []string{
		"sync: {retain_superseded: 1.5}", "sync: {retain_superseded: '1'}", "sync: {retain_superseded: -1}",
		"sync: {retain_superseded: null}", "sync: {failed_retention: 0s}", "sync: {typo: 2}",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := load(writeConfig(t, input), env(nil)); err == nil {
				t.Fatal("accepted invalid sync YAML")
			}
		})
	}
	for name, value := range map[string]string{"SYNC_RETAIN_SUPERSEDED": "2.5", "SYNC_FAILED_RETENTION": "-1h"} {
		if _, err := load("", env(map[string]string{name: value})); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("invalid %s produced %v", name, err)
		}
	}
}
