package migrations

import "embed"

// Files 包含按版本命名并嵌入二进制的 SQL 迁移文件。
//
//go:embed *.sql
var Files embed.FS
