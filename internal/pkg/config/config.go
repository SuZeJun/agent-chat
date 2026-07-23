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
	defaultLLMBaseURL           = "https://api.deepseek.com"
	defaultLLMModel             = "deepseek-v4-flash"
	defaultEmbeddingBaseURL     = "https://open.bigmodel.cn/api/paas/v4"
	defaultEmbeddingModel       = "embedding-3"
	defaultEmbeddingDimensions  = 1024
	defaultShutdownTimeout      = 10 * time.Second
	defaultDatabasePingTimeout  = 2 * time.Second
	defaultMigrationTimeout     = 30 * time.Second
	defaultWorkerPollInterval   = 2 * time.Second
	defaultLLMTimeout           = 60 * time.Second
	defaultEmbeddingTimeout     = 30 * time.Second
	defaultDatabaseMaxOpenConns = int32(10)
	defaultDatabaseMinOpenConns = int32(1)
)

// Config 汇总 API、数据库、模型和 Worker 的运行配置。
type Config struct {
	App      App
	Database Database
	Models   Models
	Worker   Worker
}

// App 定义进程级应用配置。
type App struct {
	Environment     string
	HTTPAddress     string
	LogLevel        string
	ShutdownTimeout time.Duration
}

// Database 定义 PostgreSQL 连接池和超时配置。
type Database struct {
	URL              string
	MaxOpenConns     int32
	MinOpenConns     int32
	PingTimeout      time.Duration
	MigrationTimeout time.Duration
}

// Models 汇总生成模型与 embedding 模型配置。
type Models struct {
	Chat      ChatModel
	Embedding EmbeddingModel
}

// ChatModel 定义 DeepSeek ChatModel 的连接和推理参数。
type ChatModel struct {
	APIKey   string
	BaseURL  string
	Model    string
	Thinking bool
	Timeout  time.Duration
}

// EmbeddingModel 定义智谱 Embedding 的连接和向量维度。
type EmbeddingModel struct {
	APIKey     string
	BaseURL    string
	Model      string
	Dimensions int
	Timeout    time.Duration
}

// Worker 定义后台任务轮询配置。
type Worker struct {
	PollInterval time.Duration
}

type lookupFunc func(string) (string, bool)

// Load 从进程环境变量加载并校验配置。
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup lookupFunc) (Config, error) {
	environment := strings.ToLower(valueOrDefault(lookup, "APP_ENV", "development"))
	switch environment {
	case "development", "test", "production":
	default:
		return Config{}, fmt.Errorf("APP_ENV must be one of development, test, or production")
	}

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
		Models: Models{
			Chat: ChatModel{
				APIKey:   valueOrDefault(lookup, "LLM_API_KEY", ""),
				BaseURL:  valueOrDefault(lookup, "LLM_BASE_URL", defaultLLMBaseURL),
				Model:    valueOrDefault(lookup, "LLM_MODEL", defaultLLMModel),
				Thinking: false,
				Timeout:  defaultLLMTimeout,
			},
			Embedding: EmbeddingModel{
				APIKey:     valueOrDefault(lookup, "EMBEDDING_API_KEY", ""),
				BaseURL:    valueOrDefault(lookup, "EMBEDDING_BASE_URL", defaultEmbeddingBaseURL),
				Model:      valueOrDefault(lookup, "EMBEDDING_MODEL", defaultEmbeddingModel),
				Dimensions: defaultEmbeddingDimensions,
				Timeout:    defaultEmbeddingTimeout,
			},
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
	if cfg.Models.Chat.Timeout, err = durationValue(lookup, "LLM_TIMEOUT", cfg.Models.Chat.Timeout); err != nil {
		return Config{}, err
	}
	if cfg.Models.Embedding.Timeout, err = durationValue(lookup, "EMBEDDING_TIMEOUT", cfg.Models.Embedding.Timeout); err != nil {
		return Config{}, err
	}
	if cfg.Database.MaxOpenConns, err = int32Value(lookup, "DATABASE_MAX_OPEN_CONNS", cfg.Database.MaxOpenConns); err != nil {
		return Config{}, err
	}
	if cfg.Database.MinOpenConns, err = int32Value(lookup, "DATABASE_MIN_OPEN_CONNS", cfg.Database.MinOpenConns); err != nil {
		return Config{}, err
	}
	if cfg.Models.Embedding.Dimensions, err = intValue(lookup, "EMBEDDING_DIM", cfg.Models.Embedding.Dimensions); err != nil {
		return Config{}, err
	}
	if cfg.Models.Chat.Thinking, err = boolValue(lookup, "LLM_THINKING", cfg.Models.Chat.Thinking); err != nil {
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
		if strings.TrimSpace(cfg.Models.Chat.APIKey) == "" {
			return fmt.Errorf("LLM_API_KEY must be explicitly set in production")
		}
		if strings.TrimSpace(cfg.Models.Embedding.APIKey) == "" {
			return fmt.Errorf("EMBEDDING_API_KEY must be explicitly set in production")
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
	if err := validateEndpoint("LLM_BASE_URL", cfg.Models.Chat.BaseURL, cfg.App.Environment); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Models.Chat.Model) == "" {
		return fmt.Errorf("LLM_MODEL must not be empty")
	}
	switch cfg.Models.Chat.Model {
	case "deepseek-v4-flash", "deepseek-v4-pro":
	default:
		return fmt.Errorf("LLM_MODEL must be deepseek-v4-flash or deepseek-v4-pro")
	}
	if cfg.Models.Chat.Timeout <= 0 {
		return fmt.Errorf("LLM_TIMEOUT must be greater than zero")
	}
	if err := validateEndpoint("EMBEDDING_BASE_URL", cfg.Models.Embedding.BaseURL, cfg.App.Environment); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Models.Embedding.Model) == "" {
		return fmt.Errorf("EMBEDDING_MODEL must not be empty")
	}
	if cfg.Models.Embedding.Model != defaultEmbeddingModel {
		return fmt.Errorf("EMBEDDING_MODEL must be embedding-3")
	}
	switch cfg.Models.Embedding.Dimensions {
	case 256, 512, 1024:
	default:
		return fmt.Errorf("EMBEDDING_DIM must be one of 256, 512, or 1024 for pgvector HNSW")
	}
	if cfg.Models.Embedding.Timeout <= 0 {
		return fmt.Errorf("EMBEDDING_TIMEOUT must be greater than zero")
	}
	if cfg.Worker.PollInterval <= 0 {
		return fmt.Errorf("WORKER_POLL_INTERVAL must be greater than zero")
	}
	return nil
}

func validateEndpoint(key string, rawURL string, environment string) error {
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return fmt.Errorf("%s must be a valid HTTP(S) URL", key)
	}
	if environment == "production" && endpoint.Scheme != "https" {
		return fmt.Errorf("%s must use HTTPS in production", key)
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

func intValue(lookup lookupFunc, key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	return value, nil
}

func boolValue(lookup lookupFunc, key string, fallback bool) (bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s must be a valid boolean: %w", key, err)
	}
	return value, nil
}
