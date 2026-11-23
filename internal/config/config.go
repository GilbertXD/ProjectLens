package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RepositoryConfig struct {
	AllowedLocalRoots []string `yaml:"allowed_local_roots"`
}

type GitHubConfig struct {
	APIBaseURL string        `yaml:"api_base_url"`
	Token      string        `yaml:"token"`
	Timeout    time.Duration `yaml:"timeout"`
	UserAgent  string        `yaml:"user_agent"`
}

type ScannerConfig struct {
	CacheDir        string        `yaml:"cache_dir"`
	GitTimeout      time.Duration `yaml:"git_timeout"`
	MaxFileSize     int64         `yaml:"max_file_size"`
	MaxTotalBytes   int64         `yaml:"max_total_bytes"`
	MaxFiles        int           `yaml:"max_files"`
	MaxPathLength   int           `yaml:"max_path_length"`
	MaxPathDepth    int           `yaml:"max_path_depth"`
	ExcludePatterns []string      `yaml:"exclude_patterns"`
}

func DefaultScannerConfig() ScannerConfig {
	return ScannerConfig{
		GitTimeout: 2 * time.Minute, MaxFileSize: 1 << 20, MaxTotalBytes: 50 << 20,
		MaxFiles: 10000, MaxPathLength: 512, MaxPathDepth: 32,
	}
}

func (cfg *ScannerConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: scanner must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "max_file_size", "max_total_bytes", "max_files", "max_path_length", "max_path_depth":
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.ShortTag() != "!!int" {
				return fmt.Errorf("line %d: scanner.%s must be an integer", value.Line, key.Value)
			}
		case "cache_dir", "git_timeout", "exclude_patterns":
		default:
			return fmt.Errorf("line %d: unknown scanner field %q", key.Line, key.Value)
		}
	}
	type plain ScannerConfig
	return node.Decode((*plain)(cfg))
}

func (cfg ScannerConfig) Validate() error {
	if cfg.CacheDir != "" && (!filepath.IsAbs(cfg.CacheDir) || strings.ContainsRune(cfg.CacheDir, 0)) {
		return errors.New("scanner.cache_dir (SCANNER_CACHE_DIR) must be an absolute path without NUL")
	}
	if cfg.GitTimeout <= 0 {
		return errors.New("scanner.git_timeout (SCANNER_GIT_TIMEOUT) must be positive")
	}
	if cfg.MaxFileSize <= 0 || cfg.MaxTotalBytes < cfg.MaxFileSize {
		return errors.New("scanner.max_file_size (SCANNER_MAX_FILE_SIZE) must be positive and max_total_bytes (SCANNER_MAX_TOTAL_BYTES) must be at least max_file_size")
	}
	if cfg.MaxFiles <= 0 || cfg.MaxPathLength <= 0 || cfg.MaxPathDepth <= 0 {
		return errors.New("scanner.max_files (SCANNER_MAX_FILES), max_path_length (SCANNER_MAX_PATH_LENGTH) and max_path_depth (SCANNER_MAX_PATH_DEPTH) must be positive")
	}
	for i, pattern := range cfg.ExcludePatterns {
		if strings.TrimSpace(pattern) == "" || strings.ContainsRune(pattern, 0) {
			return fmt.Errorf("scanner.exclude_patterns (SCANNER_EXCLUDE_PATTERNS) entry %d must be nonblank and contain no NUL", i+1)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("scanner.exclude_patterns (SCANNER_EXCLUDE_PATTERNS) entry %d: %w", i+1, err)
		}
	}
	return nil
}

type Config struct {
	Scanner    ScannerConfig    `yaml:"scanner"`
	Worker     WorkerConfig     `yaml:"worker"`
	Repository RepositoryConfig `yaml:"repository"`
	GitHub     GitHubConfig     `yaml:"github"`
	Database   DatabaseConfig   `yaml:"database"`
	App        AppConfig        `yaml:"app"`
	Server     ServerConfig     `yaml:"server"`
	Log        LogConfig        `yaml:"log"`
	CORS       CORSConfig       `yaml:"cors"`
}

type WorkerConfig struct {
	Mode               string        `yaml:"mode"`
	ID                 string        `yaml:"id"`
	Concurrency        int           `yaml:"concurrency"`
	PollInterval       time.Duration `yaml:"poll_interval"`
	LeaseDuration      time.Duration `yaml:"lease_duration"`
	HeartbeatInterval  time.Duration `yaml:"heartbeat_interval"`
	DefaultTimeout     time.Duration `yaml:"default_timeout"`
	DefaultMaxAttempts int           `yaml:"default_max_attempts"`
	RetryBaseDelay     time.Duration `yaml:"retry_base_delay"`
	RetryMaxDelay      time.Duration `yaml:"retry_max_delay"`
	ShutdownTimeout    time.Duration `yaml:"shutdown_timeout"`
}

func (cfg *WorkerConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: worker must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "concurrency", "default_max_attempts":
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.ShortTag() != "!!int" {
				return fmt.Errorf("line %d: worker.%s must be an integer", value.Line, key.Value)
			}
		case "mode", "id", "poll_interval", "lease_duration", "heartbeat_interval", "default_timeout", "retry_base_delay", "retry_max_delay", "shutdown_timeout":
		default:
			return fmt.Errorf("line %d: unknown worker field %q", key.Line, key.Value)
		}
	}
	type plain WorkerConfig
	return node.Decode((*plain)(cfg))
}

type DatabaseConfig struct {
	DSN               string        `yaml:"dsn"`
	MaxConns          int32         `yaml:"max_conns"`
	MinConns          int32         `yaml:"min_conns"`
	MaxConnLifetime   time.Duration `yaml:"max_conn_lifetime"`
	MaxConnIdleTime   time.Duration `yaml:"max_conn_idle_time"`
	HealthCheckPeriod time.Duration `yaml:"health_check_period"`
	ConnectTimeout    time.Duration `yaml:"connect_timeout"`
	AutoMigrate       bool          `yaml:"auto_migrate"`
}

// Check integer node types before yaml.v3 can truncate floats.
func (cfg *DatabaseConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: database must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "max_conns", "min_conns":
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.ShortTag() != "!!int" {
				return fmt.Errorf("line %d: database.%s must be an integer", value.Line, key.Value)
			}

		case "dsn", "max_conn_lifetime", "max_conn_idle_time", "health_check_period", "connect_timeout", "auto_migrate":
		default:
			return fmt.Errorf("line %d: unknown database field %q", key.Line, key.Value)
		}
	}
	type plain DatabaseConfig
	return node.Decode((*plain)(cfg))
}

type AppConfig struct {
	Name    string `yaml:"name"`
	Env     string `yaml:"env"`
	Version string `yaml:"version"`
}

type ServerConfig struct {
	Host            string        `yaml:"host"`
	Port            Port          `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type Port int

func (port *Port) UnmarshalYAML(node *yaml.Node) error {
	// yaml.v3 otherwise truncates floating-point values when decoding into integers.
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
		return fmt.Errorf("line %d: server.port must be an integer", node.Line)
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return fmt.Errorf("decode server.port: %w", err)
	}
	*port = Port(value)
	return nil
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
	AllowedMethods []string `yaml:"allowed_methods"`
	AllowedHeaders []string `yaml:"allowed_headers"`
}

func defaults() Config {
	return Config{
		Scanner: DefaultScannerConfig(),
		Worker: WorkerConfig{
			Mode:               "embedded",
			Concurrency:        4,
			PollInterval:       500 * time.Millisecond,
			LeaseDuration:      30 * time.Second,
			HeartbeatInterval:  10 * time.Second,
			DefaultTimeout:     5 * time.Minute,
			DefaultMaxAttempts: 3,
			RetryBaseDelay:     2 * time.Second,
			RetryMaxDelay:      time.Minute,
			ShutdownTimeout:    15 * time.Second,
		},
		Repository: RepositoryConfig{AllowedLocalRoots: []string{}},
		GitHub:     GitHubConfig{APIBaseURL: "https://api.github.com", Timeout: 10 * time.Second, UserAgent: "ProjectLens"},
		Database: DatabaseConfig{
			DSN:               "postgres://projectlens:projectlens@localhost:5432/projectlens?sslmode=disable",
			MaxConns:          10,
			MinConns:          2,
			MaxConnLifetime:   30 * time.Minute,
			MaxConnIdleTime:   5 * time.Minute,
			HealthCheckPeriod: time.Minute,
			ConnectTimeout:    5 * time.Second,
			AutoMigrate:       true,
		},
		App: AppConfig{Name: "ProjectLens", Env: "development", Version: "0.1.0"},
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Log: LogConfig{Level: "info", Format: "text"},
		CORS: CORSConfig{
			AllowedOrigins: []string{},
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Content-Type", "X-Request-ID"},
		},
	}
}

// Load reads a YAML file over the defaults and then applies environment overrides.
func Load(configPath string) (Config, error) {
	return load(configPath, os.LookupEnv)
}

func load(configPath string, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := defaults()
	if configPath == "" {
		configPath, _ = lookupEnv("APP_CONFIG_FILE")
	}
	if configPath != "" {
		if err := readYAML(configPath, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.applyEnv(lookupEnv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readYAML(path string, cfg *Config) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return fmt.Errorf("decode config file %q: %w", path, err)
	default:
		return fmt.Errorf("config file %q must contain a single YAML document", path)
	}
}

func (cfg *Config) applyEnv(lookupEnv func(string) (string, bool)) error {
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"SCANNER_CACHE_DIR", &cfg.Scanner.CacheDir},
		{"WORKER_MODE", &cfg.Worker.Mode},
		{"WORKER_ID", &cfg.Worker.ID},
		{"GITHUB_API_BASE_URL", &cfg.GitHub.APIBaseURL},
		{"GITHUB_TOKEN", &cfg.GitHub.Token},
		{"GITHUB_USER_AGENT", &cfg.GitHub.UserAgent},
		{"APP_NAME", &cfg.App.Name},
		{"DATABASE_URL", &cfg.Database.DSN},
		{"APP_ENV", &cfg.App.Env},
		{"APP_VERSION", &cfg.App.Version},
		{"SERVER_HOST", &cfg.Server.Host},
		{"LOG_LEVEL", &cfg.Log.Level},
		{"LOG_FORMAT", &cfg.Log.Format},
	} {
		if value, ok := lookupEnv(field.name); ok {
			*field.target = value
		}
	}

	for _, field := range []struct {
		name   string
		target *int32
	}{
		{"DB_MAX_CONNS", &cfg.Database.MaxConns},
		{"DB_MIN_CONNS", &cfg.Database.MinConns},
	} {
		if value, ok := lookupEnv(field.name); ok {
			number, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return fmt.Errorf("%s must be a 32-bit integer: %w", field.name, err)
			}
			*field.target = int32(number)
		}
	}
	for _, field := range []struct {
		name   string
		target *int
	}{
		{"SCANNER_MAX_FILES", &cfg.Scanner.MaxFiles},
		{"SCANNER_MAX_PATH_LENGTH", &cfg.Scanner.MaxPathLength},
		{"SCANNER_MAX_PATH_DEPTH", &cfg.Scanner.MaxPathDepth},
		{"WORKER_CONCURRENCY", &cfg.Worker.Concurrency},
		{"WORKER_DEFAULT_MAX_ATTEMPTS", &cfg.Worker.DefaultMaxAttempts},
	} {
		if value, ok := lookupEnv(field.name); ok {
			number, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("%s must be an integer: %w", field.name, err)
			}
			*field.target = number
		}
	}
	if value, ok := lookupEnv("DB_AUTO_MIGRATE"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("DB_AUTO_MIGRATE must be a boolean: %w", err)
		}
		cfg.Database.AutoMigrate = enabled
	}
	if value, ok := lookupEnv("SERVER_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("SERVER_PORT must be an integer: %w", err)
		}
		cfg.Server.Port = Port(port)
	}

	for _, field := range []struct {
		name   string
		target *time.Duration
	}{
		{"SCANNER_GIT_TIMEOUT", &cfg.Scanner.GitTimeout},
		{"WORKER_POLL_INTERVAL", &cfg.Worker.PollInterval},
		{"WORKER_LEASE_DURATION", &cfg.Worker.LeaseDuration},
		{"WORKER_HEARTBEAT_INTERVAL", &cfg.Worker.HeartbeatInterval},
		{"WORKER_DEFAULT_TIMEOUT", &cfg.Worker.DefaultTimeout},
		{"WORKER_RETRY_BASE_DELAY", &cfg.Worker.RetryBaseDelay},
		{"WORKER_RETRY_MAX_DELAY", &cfg.Worker.RetryMaxDelay},
		{"WORKER_SHUTDOWN_TIMEOUT", &cfg.Worker.ShutdownTimeout},
		{"GITHUB_TIMEOUT", &cfg.GitHub.Timeout},
		{"DB_MAX_CONN_LIFETIME", &cfg.Database.MaxConnLifetime},
		{"DB_MAX_CONN_IDLE_TIME", &cfg.Database.MaxConnIdleTime},
		{"DB_HEALTH_CHECK_PERIOD", &cfg.Database.HealthCheckPeriod},
		{"DB_CONNECT_TIMEOUT", &cfg.Database.ConnectTimeout},
		{"SERVER_READ_TIMEOUT", &cfg.Server.ReadTimeout},
		{"SERVER_WRITE_TIMEOUT", &cfg.Server.WriteTimeout},
		{"SERVER_IDLE_TIMEOUT", &cfg.Server.IdleTimeout},
		{"SERVER_SHUTDOWN_TIMEOUT", &cfg.Server.ShutdownTimeout},
	} {
		if value, ok := lookupEnv(field.name); ok {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("%s must be a duration: %w", field.name, err)
			}
			*field.target = duration
		}
	}

	for _, field := range []struct {
		name   string
		target *int64
	}{
		{"SCANNER_MAX_FILE_SIZE", &cfg.Scanner.MaxFileSize},
		{"SCANNER_MAX_TOTAL_BYTES", &cfg.Scanner.MaxTotalBytes},
	} {
		if value, ok := lookupEnv(field.name); ok {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("%s must be a 64-bit integer: %w", field.name, err)
			}
			*field.target = number
		}
	}
	if value, ok := lookupEnv("SCANNER_EXCLUDE_PATTERNS"); ok {
		cfg.Scanner.ExcludePatterns = nil
		if value != "" {
			for _, pattern := range strings.Split(value, ",") {
				cfg.Scanner.ExcludePatterns = append(cfg.Scanner.ExcludePatterns, strings.TrimSpace(pattern))
			}
		}
	}

	if value, ok := lookupEnv("REPO_ALLOWED_LOCAL_ROOTS"); ok {
		cfg.Repository.AllowedLocalRoots = []string{}
		if strings.TrimSpace(value) != "" {
			for _, root := range strings.Split(value, ",") {
				cfg.Repository.AllowedLocalRoots = append(cfg.Repository.AllowedLocalRoots, strings.TrimSpace(root))
			}
		}
	}
	if value, ok := lookupEnv("CORS_ALLOWED_ORIGINS"); ok {
		cfg.CORS.AllowedOrigins = []string{}
		for _, origin := range strings.Split(value, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				cfg.CORS.AllowedOrigins = append(cfg.CORS.AllowedOrigins, origin)
			}
		}
	}
	return nil
}

func (cfg Config) Validate() error {
	if err := cfg.Scanner.Validate(); err != nil {
		return err
	}
	w := cfg.Worker
	switch w.Mode {
	case "embedded", "external", "disabled":
	default:
		return errors.New("worker.mode (WORKER_MODE) must be embedded, external, or disabled")
	}
	if strings.ContainsRune(w.ID, 0) {
		return errors.New("worker.id (WORKER_ID) must not contain NUL bytes")
	}
	if w.Concurrency < 1 {
		return errors.New("worker.concurrency (WORKER_CONCURRENCY) must be positive")
	}
	if w.DefaultTimeout < time.Second || w.DefaultTimeout > 24*time.Hour {
		return errors.New("worker.default_timeout (WORKER_DEFAULT_TIMEOUT) must be between 1s and 24h")
	}
	if w.DefaultMaxAttempts < 1 || w.DefaultMaxAttempts > 100 {
		return errors.New("worker.default_max_attempts (WORKER_DEFAULT_MAX_ATTEMPTS) must be between 1 and 100")
	}

	for _, root := range cfg.Repository.AllowedLocalRoots {
		if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
			return errors.New("repository.allowed_local_roots (REPO_ALLOWED_LOCAL_ROOTS) must contain absolute paths without NUL")
		}
	}
	endpoint, err := url.Parse(cfg.GitHub.APIBaseURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return errors.New("github.api_base_url (GITHUB_API_BASE_URL) must be an HTTP or HTTPS URL with a host")
	}
	if cfg.GitHub.Timeout <= 0 {
		return errors.New("github.timeout (GITHUB_TIMEOUT) must be positive")
	}
	if strings.TrimSpace(cfg.GitHub.UserAgent) == "" {
		return errors.New("github.user_agent (GITHUB_USER_AGENT) must not be blank")
	}
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errors.New("database.dsn (DATABASE_URL) must not be empty")
	}
	if cfg.Database.MaxConns < 1 {
		return errors.New("database.max_conns (DB_MAX_CONNS) must be positive")
	}
	if cfg.Database.MinConns < 0 || cfg.Database.MinConns > cfg.Database.MaxConns {
		return errors.New("database.min_conns (DB_MIN_CONNS) must be between zero and max_conns")
	}
	if strings.TrimSpace(cfg.App.Name) == "" {
		return errors.New("app.name (APP_NAME) must not be empty")
	}
	switch cfg.App.Env {
	case "development", "test", "staging", "production":
	default:
		return fmt.Errorf("app.env (APP_ENV) %q must be development, test, staging, or production", cfg.App.Env)
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port (SERVER_PORT) %d must be between 1 and 65535", cfg.Server.Port)
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"worker.poll_interval (WORKER_POLL_INTERVAL)", cfg.Worker.PollInterval},
		{"worker.lease_duration (WORKER_LEASE_DURATION)", cfg.Worker.LeaseDuration},
		{"worker.heartbeat_interval (WORKER_HEARTBEAT_INTERVAL)", cfg.Worker.HeartbeatInterval},
		{"worker.default_timeout (WORKER_DEFAULT_TIMEOUT)", cfg.Worker.DefaultTimeout},
		{"worker.retry_base_delay (WORKER_RETRY_BASE_DELAY)", cfg.Worker.RetryBaseDelay},
		{"worker.retry_max_delay (WORKER_RETRY_MAX_DELAY)", cfg.Worker.RetryMaxDelay},
		{"worker.shutdown_timeout (WORKER_SHUTDOWN_TIMEOUT)", cfg.Worker.ShutdownTimeout},
		{"database.max_conn_lifetime (DB_MAX_CONN_LIFETIME)", cfg.Database.MaxConnLifetime},
		{"database.max_conn_idle_time (DB_MAX_CONN_IDLE_TIME)", cfg.Database.MaxConnIdleTime},
		{"database.health_check_period (DB_HEALTH_CHECK_PERIOD)", cfg.Database.HealthCheckPeriod},
		{"database.connect_timeout (DB_CONNECT_TIMEOUT)", cfg.Database.ConnectTimeout},
		{"server.read_timeout (SERVER_READ_TIMEOUT)", cfg.Server.ReadTimeout},
		{"server.write_timeout (SERVER_WRITE_TIMEOUT)", cfg.Server.WriteTimeout},
		{"server.idle_timeout (SERVER_IDLE_TIMEOUT)", cfg.Server.IdleTimeout},
		{"server.shutdown_timeout (SERVER_SHUTDOWN_TIMEOUT)", cfg.Server.ShutdownTimeout},
	} {
		if field.value <= 0 {
			return fmt.Errorf("%s must be positive, got %s", field.name, field.value)
		}
	}
	if w.HeartbeatInterval >= w.LeaseDuration {
		return errors.New("worker.heartbeat_interval (WORKER_HEARTBEAT_INTERVAL) must be less than lease_duration")
	}
	if w.RetryMaxDelay < w.RetryBaseDelay {
		return errors.New("worker.retry_max_delay (WORKER_RETRY_MAX_DELAY) must be at least retry_base_delay")
	}
	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level (LOG_LEVEL) %q must be debug, info, warn, or error", cfg.Log.Level)
	}
	switch cfg.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format (LOG_FORMAT) %q must be text or json", cfg.Log.Format)
	}
	return nil
}
