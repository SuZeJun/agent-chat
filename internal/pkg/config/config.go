package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddress          = ":8080"
	defaultDatabaseURL          = "postgres://agent_chat:agent_chat_password@127.0.0.1:5433/agent_chat?sslmode=disable"
	defaultShutdownTimeout      = 10 * time.Second
	defaultDatabasePingTimeout  = 2 * time.Second
	defaultMigrationTimeout     = 30 * time.Second
	defaultWorkerPollInterval   = 2 * time.Second
	defaultDatabaseMaxOpenConns = int32(10)
	defaultDatabaseMinOpenConns = int32(1)
)

type Config struct {
	App      App
	Database Database
	Worker   Worker
}

type App struct {
	Environment     string
	HTTPAddress     string
	LogLevel        string
	ShutdownTimeout time.Duration
}

type Database struct {
	URL              string
	MaxOpenConns     int32
	MinOpenConns     int32
	PingTimeout      time.Duration
	MigrationTimeout time.Duration
}

type Worker struct {
	PollInterval time.Duration
}

type lookupFunc func(string) (string, bool)

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup lookupFunc) (Config, error) {
	environment := strings.ToLower(valueOrDefault(lookup, "APP_ENV", "development"))
	databaseURL, databaseURLSet := explicitValue(lookup, "DATABASE_URL")
	if !databaseURLSet {
		switch environment {
		case "development", "test":
			databaseURL = defaultDatabaseURL
		default:
			return Config{}, fmt.Errorf("DATABASE_URL must be explicitly set when APP_ENV=%s", environment)
		}
	}

	cfg := Config{
		App: App{
			Environment:     environment,
			HTTPAddress:     valueOrDefault(lookup, "HTTP_ADDR", defaultHTTPAddress),
			LogLevel:        strings.ToLower(valueOrDefault(lookup, "LOG_LEVEL", "info")),
			ShutdownTimeout: defaultShutdownTimeout,
		},
		Database: Database{
			URL:              databaseURL,
			MaxOpenConns:     defaultDatabaseMaxOpenConns,
			MinOpenConns:     defaultDatabaseMinOpenConns,
			PingTimeout:      defaultDatabasePingTimeout,
			MigrationTimeout: defaultMigrationTimeout,
		},
		Worker: Worker{
			PollInterval: defaultWorkerPollInterval,
		},
	}

	var err error
	if cfg.App.ShutdownTimeout, err = durationValue(lookup, "SHUTDOWN_TIMEOUT", cfg.App.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Database.PingTimeout, err = durationValue(lookup, "DATABASE_PING_TIMEOUT", cfg.Database.PingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Database.MigrationTimeout, err = durationValue(lookup, "DATABASE_MIGRATION_TIMEOUT", cfg.Database.MigrationTimeout); err != nil {
		return Config{}, err
	}
	if cfg.Worker.PollInterval, err = durationValue(lookup, "WORKER_POLL_INTERVAL", cfg.Worker.PollInterval); err != nil {
		return Config{}, err
	}
	if cfg.Database.MaxOpenConns, err = int32Value(lookup, "DATABASE_MAX_OPEN_CONNS", cfg.Database.MaxOpenConns); err != nil {
		return Config{}, err
	}
	if cfg.Database.MinOpenConns, err = int32Value(lookup, "DATABASE_MIN_OPEN_CONNS", cfg.Database.MinOpenConns); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.App.HTTPAddress) == "" {
		return fmt.Errorf("HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return fmt.Errorf("DATABASE_URL must not be empty")
	}
	if cfg.App.Environment == "production" {
		databaseURL, err := url.Parse(cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("DATABASE_URL must be a valid URL: %w", err)
		}
		sslMode := strings.ToLower(databaseURL.Query().Get("sslmode"))
		switch sslMode {
		case "require", "verify-ca", "verify-full":
		default:
			return fmt.Errorf("DATABASE_URL must use sslmode=require, verify-ca, or verify-full in production")
		}
	}
	switch cfg.App.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error")
	}
	if cfg.App.ShutdownTimeout <= 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if cfg.Database.PingTimeout <= 0 {
		return fmt.Errorf("DATABASE_PING_TIMEOUT must be greater than zero")
	}
	if cfg.Database.MigrationTimeout <= 0 {
		return fmt.Errorf("DATABASE_MIGRATION_TIMEOUT must be greater than zero")
	}
	if cfg.Database.MaxOpenConns <= 0 {
		return fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be greater than zero")
	}
	if cfg.Database.MinOpenConns < 0 {
		return fmt.Errorf("DATABASE_MIN_OPEN_CONNS must not be negative")
	}
	if cfg.Database.MinOpenConns > cfg.Database.MaxOpenConns {
		return fmt.Errorf("DATABASE_MIN_OPEN_CONNS must not exceed DATABASE_MAX_OPEN_CONNS")
	}
	if cfg.Worker.PollInterval <= 0 {
		return fmt.Errorf("WORKER_POLL_INTERVAL must be greater than zero")
	}
	return nil
}

func valueOrDefault(lookup lookupFunc, key string, fallback string) string {
	value, ok := explicitValue(lookup, key)
	if !ok {
		return fallback
	}
	return value
}

func explicitValue(lookup lookupFunc, key string) (string, bool) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func durationValue(lookup lookupFunc, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}
	return value, nil
}

func int32Value(lookup lookupFunc, key string, fallback int32) (int32, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	return int32(value), nil
}
