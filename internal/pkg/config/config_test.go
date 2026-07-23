package config

import (
	"testing"
	"time"
)

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := load(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.App.HTTPAddress != defaultHTTPAddress {
		t.Fatalf("unexpected HTTP address: %q", cfg.App.HTTPAddress)
	}
	if cfg.Database.URL != defaultDatabaseURL {
		t.Fatalf("unexpected database URL: %q", cfg.Database.URL)
	}
	if cfg.Worker.PollInterval != defaultWorkerPollInterval {
		t.Fatalf("unexpected poll interval: %s", cfg.Worker.PollInterval)
	}
	if cfg.Models.Chat.BaseURL != defaultLLMBaseURL || cfg.Models.Chat.Model != defaultLLMModel {
		t.Fatalf("unexpected chat model config: %#v", cfg.Models.Chat)
	}
	if cfg.Models.Chat.Thinking {
		t.Fatal("chat model thinking must be disabled by default")
	}
	if cfg.Models.Embedding.Model != defaultEmbeddingModel || cfg.Models.Embedding.Dimensions != defaultEmbeddingDimensions {
		t.Fatalf("unexpected embedding config: %#v", cfg.Models.Embedding)
	}
}

func TestLoadParsesOverrides(t *testing.T) {
	values := map[string]string{
		"APP_ENV":                    "test",
		"HTTP_ADDR":                  ":9090",
		"LOG_LEVEL":                  "debug",
		"SHUTDOWN_TIMEOUT":           "5s",
		"DATABASE_URL":               "postgres://example",
		"DATABASE_MAX_OPEN_CONNS":    "20",
		"DATABASE_MIN_OPEN_CONNS":    "2",
		"DATABASE_PING_TIMEOUT":      "3s",
		"DATABASE_MIGRATION_TIMEOUT": "45s",
		"WORKER_POLL_INTERVAL":       "500ms",
		"LLM_API_KEY":                "chat-key",
		"LLM_BASE_URL":               "https://llm.example.com/v1",
		"LLM_MODEL":                  "deepseek-v4-pro",
		"LLM_THINKING":               "true",
		"LLM_TIMEOUT":                "15s",
		"EMBEDDING_API_KEY":          "embedding-key",
		"EMBEDDING_BASE_URL":         "https://embedding.example.com/v1",
		"EMBEDDING_MODEL":            "embedding-3",
		"EMBEDDING_DIM":              "512",
		"EMBEDDING_TIMEOUT":          "8s",
	}
	cfg, err := load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.App.Environment != "test" || cfg.App.HTTPAddress != ":9090" || cfg.App.LogLevel != "debug" {
		t.Fatalf("unexpected app config: %#v", cfg.App)
	}
	if cfg.App.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected shutdown timeout: %s", cfg.App.ShutdownTimeout)
	}
	if cfg.Database.MaxOpenConns != 20 || cfg.Database.MinOpenConns != 2 {
		t.Fatalf("unexpected connection limits: %#v", cfg.Database)
	}
	if cfg.Worker.PollInterval != 500*time.Millisecond {
		t.Fatalf("unexpected poll interval: %s", cfg.Worker.PollInterval)
	}
	if cfg.Models.Chat.APIKey != "chat-key" || cfg.Models.Chat.Model != "deepseek-v4-pro" || !cfg.Models.Chat.Thinking {
		t.Fatalf("unexpected chat model config: %#v", cfg.Models.Chat)
	}
	if cfg.Models.Chat.Timeout != 15*time.Second {
		t.Fatalf("unexpected chat model timeout: %s", cfg.Models.Chat.Timeout)
	}
	if cfg.Models.Embedding.APIKey != "embedding-key" || cfg.Models.Embedding.Dimensions != 512 {
		t.Fatalf("unexpected embedding config: %#v", cfg.Models.Embedding)
	}
	if cfg.Models.Embedding.Timeout != 8*time.Second {
		t.Fatalf("unexpected embedding timeout: %s", cfg.Models.Embedding.Timeout)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "log level", key: "LOG_LEVEL", value: "trace"},
		{name: "duration", key: "SHUTDOWN_TIMEOUT", value: "later"},
		{name: "connections", key: "DATABASE_MAX_OPEN_CONNS", value: "0"},
		{name: "thinking", key: "LLM_THINKING", value: "sometimes"},
		{name: "embedding dimensions", key: "EMBEDDING_DIM", value: "2048"},
		{name: "chat model", key: "LLM_MODEL", value: "unknown-model"},
		{name: "embedding model", key: "EMBEDDING_MODEL", value: "embedding-2"},
		{name: "environment", key: "APP_ENV", value: "prod"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := load(func(key string) (string, bool) {
				if key == test.key {
					return test.value, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadRejectsUnknownEnvironmentBeforeApplyingDevelopmentDefaults(t *testing.T) {
	for _, environment := range []string{"prod", "prodution", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, err := load(func(key string) (string, bool) {
				if key == "APP_ENV" {
					return environment, true
				}
				return "", false
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadRequiresProductionDatabaseURL(t *testing.T) {
	_, err := load(func(key string) (string, bool) {
		if key == "APP_ENV" {
			return "production", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadRejectsInsecureProductionDatabaseSSL(t *testing.T) {
	for _, sslMode := range []string{"", "disable", "allow", "prefer"} {
		t.Run(sslMode, func(t *testing.T) {
			values := map[string]string{
				"APP_ENV":           "production",
				"DATABASE_URL":      "postgres://example/agent_chat?sslmode=" + sslMode,
				"LLM_API_KEY":       "chat-key",
				"EMBEDDING_API_KEY": "embedding-key",
			}
			_, err := load(func(key string) (string, bool) {
				value, ok := values[key]
				return value, ok
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadAcceptsSecureProductionDatabaseSSL(t *testing.T) {
	for _, sslMode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(sslMode, func(t *testing.T) {
			values := map[string]string{
				"APP_ENV":           "production",
				"DATABASE_URL":      "postgres://example/agent_chat?sslmode=" + sslMode,
				"LLM_API_KEY":       "chat-key",
				"EMBEDDING_API_KEY": "embedding-key",
			}
			if _, err := load(func(key string) (string, bool) {
				value, ok := values[key]
				return value, ok
			}); err != nil {
				t.Fatalf("load returned error: %v", err)
			}
		})
	}
}

func TestLoadRequiresProductionModelKeys(t *testing.T) {
	values := map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://example/agent_chat?sslmode=require",
	}
	if _, err := load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadRejectsInsecureProductionModelEndpoint(t *testing.T) {
	values := map[string]string{
		"APP_ENV":           "production",
		"DATABASE_URL":      "postgres://example/agent_chat?sslmode=require",
		"LLM_API_KEY":       "chat-key",
		"LLM_BASE_URL":      "http://llm.example.com",
		"EMBEDDING_API_KEY": "embedding-key",
	}
	if _, err := load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}); err == nil {
		t.Fatal("expected an error")
	}
}
