package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"agent-chat/internal/infrastructure/persistence"
	"agent-chat/internal/pkg/config"
	"agent-chat/internal/pkg/logx"

	"github.com/jackc/pgx/v5/pgxpool"
)

type runtime struct {
	config   config.Config
	logger   *slog.Logger
	database *pgxpool.Pool
}

func newRuntime(ctx context.Context, output io.Writer) (*runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	logger := logx.New(output, cfg.App.LogLevel)
	database, err := persistence.Open(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}

	migrationContext, cancel := context.WithTimeout(ctx, cfg.Database.MigrationTimeout)
	defer cancel()
	if err := persistence.Migrate(migrationContext, database); err != nil {
		database.Close()
		return nil, err
	}

	return &runtime{
		config:   cfg,
		logger:   logger,
		database: database,
	}, nil
}

func (runtime *runtime) close() {
	runtime.database.Close()
}
