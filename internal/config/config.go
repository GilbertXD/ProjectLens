package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
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

type Config struct {
	Repository RepositoryConfig `yaml:"repository"`
	GitHub     GitHubConfig     `yaml:"github"`
	Database   DatabaseConfig   `yaml:"database"`
	App        AppConfig        `yaml:"app"`
	Server     ServerConfig     `yaml:"server"`
	Log        LogConfig        `yaml:"log"`
	CORS       CORSConfig       `yaml:"cors"`
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
