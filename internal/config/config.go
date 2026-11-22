package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App    AppConfig    `yaml:"app"`
	Server ServerConfig `yaml:"server"`
	Log    LogConfig    `yaml:"log"`
	CORS   CORSConfig   `yaml:"cors"`
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
			AllowedMethods: []string{"GET", "OPTIONS"},
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
		{"APP_NAME", &cfg.App.Name},
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
