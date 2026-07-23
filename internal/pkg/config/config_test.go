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
				"APP_ENV":      "production",
				"DATABASE_URL": "postgres://example/agent_chat?sslmode=" + sslMode,
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
				"APP_ENV":      "production",
				"DATABASE_URL": "postgres://example/agent_chat?sslmode=" + sslMode,
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
