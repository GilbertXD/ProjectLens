package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

type RepositoryConfig struct {
	AllowedLocalRoots []string `yaml:"allowed_local_roots"`
}

type GitHubConfig struct {
	GitBaseURL string        `yaml:"git_base_url"`
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

type SyncConfig struct {
	RetainSuperseded int           `yaml:"retain_superseded"`
	FailedRetention  time.Duration `yaml:"failed_retention"`
}

func (cfg *SyncConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: sync must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "retain_superseded":
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.ShortTag() != "!!int" {
				return fmt.Errorf("line %d: sync.retain_superseded must be an integer", value.Line)
			}
		case "failed_retention":
		default:
			return fmt.Errorf("line %d: unknown sync field %q", key.Line, key.Value)
		}
	}
	type plain SyncConfig
	return node.Decode((*plain)(cfg))
}

func (cfg SyncConfig) Validate() error {
	if cfg.RetainSuperseded < 0 {
		return errors.New("sync.retain_superseded (SYNC_RETAIN_SUPERSEDED) must be nonnegative")
	}
	if cfg.FailedRetention <= 0 {
		return errors.New("sync.failed_retention (SYNC_FAILED_RETENTION) must be positive")
	}
	return nil
}

const (
	DefaultDocumentMaxContentBytes int64 = 1 << 20
	MaxDocumentContentBytesCeiling int64 = 1 << 20
)

type DocumentConfig struct {
	MaxContentBytes int64 `yaml:"max_content_bytes"`
}

func (cfg *DocumentConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: document must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value != "max_content_bytes" {
			return fmt.Errorf("line %d: unknown document field %q", key.Line, key.Value)
		}
		if value.Kind == yaml.AliasNode {
			value = value.Alias
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!int" {
			return fmt.Errorf("line %d: document.max_content_bytes must be an integer", value.Line)
		}
	}
	type plain DocumentConfig
	return node.Decode((*plain)(cfg))
}

type RedisConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Addr         string        `yaml:"addr"`
	Password     string        `yaml:"password"`
	DB           int           `yaml:"db"`
	KeyPrefix    string        `yaml:"key_prefix"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

func (cfg *RedisConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain RedisConfig
	return decodeStrictSection(node, (*plain)(cfg), "redis", []string{"db"},
		[]string{"enabled", "addr", "password", "key_prefix", "dial_timeout", "read_timeout", "write_timeout"})
}

func (cfg RedisConfig) Validate() error {
	host, port, err := net.SplitHostPort(cfg.Addr)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || strings.TrimSpace(host) == "" || portErr != nil || portNumber < 1 || portNumber > 65535 || strings.ContainsRune(cfg.Addr, 0) {
		return errors.New("redis.addr (REDIS_ADDR) must include a host and valid port")
	}
	if cfg.DB < 0 || cfg.DB > 15 {
		return errors.New("redis.db (REDIS_DB) must be between 0 and 15")
	}
	if strings.TrimSpace(cfg.KeyPrefix) == "" || strings.ContainsRune(cfg.KeyPrefix, 0) {
		return errors.New("redis.key_prefix (REDIS_KEY_PREFIX) must be nonblank and contain no NUL")
	}
	if cfg.DialTimeout <= 0 || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 {
		return errors.New("redis dial_timeout, read_timeout and write_timeout must be positive")
	}
	return nil
}

type SearchConfig struct {
	DefaultLimit   int           `yaml:"default_limit"`
	MaxLimit       int           `yaml:"max_limit"`
	MaxQueryLength int           `yaml:"max_query_length"`
	CacheTTL       time.Duration `yaml:"cache_ttl"`
}

func DefaultSearchConfig() SearchConfig {
	return SearchConfig{DefaultLimit: 20, MaxLimit: 100, MaxQueryLength: 200, CacheTTL: 5 * time.Minute}
}

func (cfg *SearchConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain SearchConfig
	return decodeStrictSection(node, (*plain)(cfg), "search",
		[]string{"default_limit", "max_limit", "max_query_length"}, []string{"cache_ttl"})
}

func (cfg SearchConfig) Validate() error {
	if cfg.DefaultLimit < 1 || cfg.MaxLimit < cfg.DefaultLimit {
		return errors.New("search.default_limit (SEARCH_DEFAULT_LIMIT) must be positive and at most max_limit (SEARCH_MAX_LIMIT)")
	}
	if cfg.MaxQueryLength < 1 || cfg.CacheTTL <= 0 {
		return errors.New("search.max_query_length (SEARCH_MAX_QUERY_LENGTH) and cache_ttl (SEARCH_CACHE_TTL) must be positive")
	}
	return nil
}

func decodeStrictSection(node *yaml.Node, cfg any, section string, integers, otherFields []string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s must be a mapping", node.Line, section)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch {
		case slices.Contains(integers, key.Value):
			value = resolveYAMLAlias(value)
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!int" {
				return fmt.Errorf("line %d: %s.%s must be an integer", value.Line, section, key.Value)
			}
		case slices.Contains(otherFields, key.Value):
		default:
			return fmt.Errorf("line %d: unknown %s field %q", key.Line, section, key.Value)
		}
	}
	return node.Decode(cfg)
}

type AuthConfig struct {
	Enabled      bool     `yaml:"enabled"`
	BearerTokens []string `yaml:"bearer_tokens"`
}

func (cfg *AuthConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: auth must be a mapping", node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], resolveYAMLAlias(node.Content[i+1])
		switch key.Value {
		case "enabled":
			if value.ShortTag() != "!!bool" {
				return fmt.Errorf("line %d: auth.enabled must be a boolean", value.Line)
			}
		case "bearer_tokens":
			if value.Kind != yaml.SequenceNode {
				return fmt.Errorf("line %d: auth.bearer_tokens must be a sequence", value.Line)
			}
			for _, token := range value.Content {
				if resolveYAMLAlias(token).ShortTag() != "!!str" {
					return fmt.Errorf("line %d: auth.bearer_tokens entries must be strings", token.Line)
				}
			}
		default:
			return fmt.Errorf("line %d: unknown auth field", key.Line)
		}
	}
	type plain AuthConfig
	if err := node.Decode((*plain)(cfg)); err != nil {
		// The YAML decoder may include the supplied scalar in an error.
		return fmt.Errorf("line %d: cannot decode auth settings", node.Line)
	}
	return nil
}

func resolveYAMLAlias(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

func (cfg AuthConfig) Validate() error {
	if cfg.Enabled && len(cfg.BearerTokens) == 0 {
		return errors.New("auth.bearer_tokens (AUTH_BEARER_TOKENS) must contain at least one token when auth is enabled")
	}
	seen := make(map[string]struct{}, len(cfg.BearerTokens))
	for i, token := range cfg.BearerTokens {
		if token == "" || strings.ContainsRune(token, 0) || strings.ContainsFunc(token, unicode.IsSpace) {
			return fmt.Errorf("auth.bearer_tokens (AUTH_BEARER_TOKENS) entry %d must be non-empty and contain no whitespace or NUL", i+1)
		}
		if _, duplicate := seen[token]; duplicate {
			return fmt.Errorf("auth.bearer_tokens (AUTH_BEARER_TOKENS) entry %d is a duplicate", i+1)
		}
		seen[token] = struct{}{}
	}
	return nil
}

type ScheduleConfig struct {
	MinIntervalSeconds int `yaml:"min_interval_seconds"`
	Batch              int `yaml:"batch"`
}
type ObservabilityConfig struct {
	MetricsAddr string `yaml:"metrics_addr"`
	Trace       bool   `yaml:"trace"`
}
type Config struct {
	Observability ObservabilityConfig `yaml:"observability"`
	Schedule      ScheduleConfig      `yaml:"schedule"`
	Auth          AuthConfig          `yaml:"auth"`
	Redis         RedisConfig         `yaml:"redis"`
	Search        SearchConfig        `yaml:"search"`
	Document      DocumentConfig      `yaml:"document"`
	Sync          SyncConfig          `yaml:"sync"`
	Scanner       ScannerConfig       `yaml:"scanner"`
	Worker        WorkerConfig        `yaml:"worker"`
	Repository    RepositoryConfig    `yaml:"repository"`
	GitHub        GitHubConfig        `yaml:"github"`
	Database      DatabaseConfig      `yaml:"database"`
	App           AppConfig           `yaml:"app"`
	Server        ServerConfig        `yaml:"server"`
	Log           LogConfig           `yaml:"log"`
	CORS          CORSConfig          `yaml:"cors"`
}

type WorkerConfig struct {
	EnableMockJobs     bool          `yaml:"enable_mock_jobs"`
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
		case "enable_mock_jobs", "mode", "id", "poll_interval", "lease_duration", "heartbeat_interval", "default_timeout", "retry_base_delay", "retry_max_delay", "shutdown_timeout":
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
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	RequestTimeout    time.Duration `yaml:"request_timeout"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes"`
	MaxBodyBytes      int64         `yaml:"max_body_bytes"`
	MaxInFlight       int           `yaml:"max_in_flight"`
	Host              string        `yaml:"host"`
	Port              Port          `yaml:"port"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

func (cfg *ServerConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain ServerConfig
	return decodeStrictSection(node, (*plain)(cfg), "server",
		[]string{"port", "max_header_bytes", "max_body_bytes", "max_in_flight"},
		[]string{"host", "read_header_timeout", "request_timeout", "read_timeout", "write_timeout", "idle_timeout", "shutdown_timeout"})
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
		Schedule: ScheduleConfig{MinIntervalSeconds: 60, Batch: 4},
		Auth:     AuthConfig{BearerTokens: []string{}},
		Redis: RedisConfig{
			Enabled: true, Addr: "localhost:6379", KeyPrefix: "projectlens",
			DialTimeout: 300 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond,
		},
		Search:   DefaultSearchConfig(),
		Document: DocumentConfig{MaxContentBytes: DefaultDocumentMaxContentBytes},
		Scanner:  DefaultScannerConfig(),
		Sync:     SyncConfig{RetainSuperseded: 2, FailedRetention: 24 * time.Hour},
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
			ReadHeaderTimeout: 5 * time.Second,
			RequestTimeout:    8 * time.Second,
			MaxHeaderBytes:    65536,
			MaxBodyBytes:      4194304,
			MaxInFlight:       128,
			Host:              "0.0.0.0",
			Port:              8080,
			ReadTimeout:       5 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   10 * time.Second,
		},
		Log: LogConfig{Level: "info", Format: "text"},
		CORS: CORSConfig{
			AllowedOrigins: []string{},
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-ID"},
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
		{"REDIS_ADDR", &cfg.Redis.Addr},
		{"REDIS_PASSWORD", &cfg.Redis.Password},
		{"REDIS_KEY_PREFIX", &cfg.Redis.KeyPrefix},
		{"SCANNER_CACHE_DIR", &cfg.Scanner.CacheDir},
		{"WORKER_MODE", &cfg.Worker.Mode},
		{"WORKER_ID", &cfg.Worker.ID},
		{"GITHUB_GIT_BASE_URL", &cfg.GitHub.GitBaseURL},
		{"GITHUB_API_BASE_URL", &cfg.GitHub.APIBaseURL},
		{"GITHUB_TOKEN", &cfg.GitHub.Token},
		{"GITHUB_USER_AGENT", &cfg.GitHub.UserAgent},
		{"APP_NAME", &cfg.App.Name},
		{"DATABASE_URL", &cfg.Database.DSN},
		{"METRICS_ADDR", &cfg.Observability.MetricsAddr},
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
		{"SERVER_MAX_HEADER_BYTES", &cfg.Server.MaxHeaderBytes},
		{"SERVER_MAX_IN_FLIGHT", &cfg.Server.MaxInFlight},
		{"REDIS_DB", &cfg.Redis.DB},
		{"SEARCH_DEFAULT_LIMIT", &cfg.Search.DefaultLimit},
		{"SEARCH_MAX_LIMIT", &cfg.Search.MaxLimit},
		{"SEARCH_MAX_QUERY_LENGTH", &cfg.Search.MaxQueryLength},
		{"SYNC_RETAIN_SUPERSEDED", &cfg.Sync.RetainSuperseded},
		{"SCANNER_MAX_FILES", &cfg.Scanner.MaxFiles},
		{"SCANNER_MAX_PATH_LENGTH", &cfg.Scanner.MaxPathLength},
		{"SCANNER_MAX_PATH_DEPTH", &cfg.Scanner.MaxPathDepth},
		{"WORKER_CONCURRENCY", &cfg.Worker.Concurrency},
		{"SCHEDULE_MIN_INTERVAL_SECONDS", &cfg.Schedule.MinIntervalSeconds}, {"SCHEDULE_BATCH", &cfg.Schedule.Batch},
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
	for _, field := range []struct {
		name   string
		target *bool
	}{
		{"AUTH_ENABLED", &cfg.Auth.Enabled},
		{"DB_AUTO_MIGRATE", &cfg.Database.AutoMigrate},
		{"WORKER_ENABLE_MOCK_JOBS", &cfg.Worker.EnableMockJobs},
		{"TRACE_ENABLED", &cfg.Observability.Trace},
		{"REDIS_ENABLED", &cfg.Redis.Enabled},
	} {
		if value, ok := lookupEnv(field.name); ok {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("%s must be a boolean", field.name)
			}
			*field.target = enabled
		}
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
		{"REDIS_DIAL_TIMEOUT", &cfg.Redis.DialTimeout},
		{"REDIS_READ_TIMEOUT", &cfg.Redis.ReadTimeout},
		{"REDIS_WRITE_TIMEOUT", &cfg.Redis.WriteTimeout},
		{"SEARCH_CACHE_TTL", &cfg.Search.CacheTTL},
		{"SYNC_FAILED_RETENTION", &cfg.Sync.FailedRetention},
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
		{"SERVER_READ_HEADER_TIMEOUT", &cfg.Server.ReadHeaderTimeout},
		{"SERVER_REQUEST_TIMEOUT", &cfg.Server.RequestTimeout},
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
		{"SERVER_MAX_BODY_BYTES", &cfg.Server.MaxBodyBytes},
		{"DOCUMENT_MAX_CONTENT_BYTES", &cfg.Document.MaxContentBytes},
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
	if value, ok := lookupEnv("AUTH_BEARER_TOKENS"); ok {
		cfg.Auth.BearerTokens = []string{}
		if value != "" {
			cfg.Auth.BearerTokens = strings.Split(value, ",")
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

func validHTTPBaseURL(raw string) bool {
	endpoint, err := url.Parse(raw)
	return err == nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Hostname() != ""
}

func (cfg Config) Validate() error {
	if err := cfg.Auth.Validate(); err != nil {
		return err
	}
	if err := cfg.Redis.Validate(); err != nil {
		return err
	}
	if err := cfg.Search.Validate(); err != nil {
		return err
	}
	if cfg.Document.MaxContentBytes < 1 || cfg.Document.MaxContentBytes > MaxDocumentContentBytesCeiling {
		return fmt.Errorf("document.max_content_bytes (DOCUMENT_MAX_CONTENT_BYTES) must be between 1 and %d", MaxDocumentContentBytesCeiling)
	}
	if err := cfg.Sync.Validate(); err != nil {
		return err
	}
	if err := cfg.Scanner.Validate(); err != nil {
		return err
	}
	if cfg.Schedule.MinIntervalSeconds < 1 || cfg.Schedule.MinIntervalSeconds > 31536000 || cfg.Schedule.Batch < 1 || cfg.Schedule.Batch > 100 {
		return errors.New("schedule interval or batch outside allowed range")
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
	if !validHTTPBaseURL(cfg.GitHub.APIBaseURL) {
		return errors.New("github.api_base_url (GITHUB_API_BASE_URL) must be an HTTP or HTTPS URL with a host")
	}
	if cfg.GitHub.GitBaseURL != "" && !validHTTPBaseURL(cfg.GitHub.GitBaseURL) {
		return errors.New("github.git_base_url (GITHUB_GIT_BASE_URL) must be an HTTP or HTTPS URL with a host")
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
	if cfg.Server.RequestTimeout > cfg.Server.WriteTimeout {
		return errors.New("server.request_timeout (SERVER_REQUEST_TIMEOUT) must be at most write_timeout")
	}
	if cfg.Server.MaxHeaderBytes < 1024 || cfg.Server.MaxHeaderBytes > 1048576 {
		return errors.New("server.max_header_bytes (SERVER_MAX_HEADER_BYTES) must be between 1024 and 1048576")
	}
	if cfg.Server.MaxBodyBytes < 1024 || cfg.Server.MaxBodyBytes > 67108864 {
		return errors.New("server.max_body_bytes (SERVER_MAX_BODY_BYTES) must be between 1024 and 67108864")
	}
	if cfg.Server.MaxInFlight < 0 || cfg.Server.MaxInFlight > 10000 {
		return errors.New("server.max_in_flight (SERVER_MAX_IN_FLIGHT) must be between 0 and 10000")
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"server.read_header_timeout (SERVER_READ_HEADER_TIMEOUT)", cfg.Server.ReadHeaderTimeout},
		{"server.request_timeout (SERVER_REQUEST_TIMEOUT)", cfg.Server.RequestTimeout},
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
