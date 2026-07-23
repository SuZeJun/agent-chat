package persistence

import (
	"context"
	"fmt"

	"agent-chat/internal/pkg/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open 创建 PostgreSQL 连接池，并在返回前完成一次带超时的连通性检查。
func Open(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = cfg.MaxOpenConns
	poolConfig.MinConns = cfg.MinOpenConns

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	pingContext, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
