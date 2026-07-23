package knowledge

import (
	"context"
	"time"
)

// Repository 定义知识版本写入、原子发布和活动切片检索能力。
type Repository interface {
	// CreateBase 创建知识库。
	CreateBase(ctx context.Context, base Base) error
	// CreateDocument 创建尚未发布版本的逻辑文档。
	CreateDocument(ctx context.Context, document Document) error
	// CreateVersionAndIndexJob 原子创建不可变版本和持久化索引任务。
	CreateVersionAndIndexJob(ctx context.Context, version Version, jobID string) error
	// LoadIndexSource 读取 Worker 构建索引所需的逻辑文档与不可变版本快照。
	LoadIndexSource(ctx context.Context, versionID string) (IndexSource, error)
	// ReplaceChunksAndMarkReady 原子替换未发布版本切片并将版本标记为 ready。
	ReplaceChunksAndMarkReady(
		ctx context.Context,
		versionID string,
		identity EmbeddingIdentity,
		chunks []Chunk,
		indexedAt time.Time,
	) error
	// MarkVersionFailed 记录可安全展示的索引失败原因。
	MarkVersionFailed(ctx context.Context, versionID string, reason string) error
	// PublishVersion 将 ready 版本原子切换为文档活动版本。
	PublishVersion(
		ctx context.Context,
		documentID string,
		versionID string,
		identity EmbeddingIdentity,
		publishedAt time.Time,
	) error
	// SearchActiveChunks 仅检索当前活动版本，并拒绝混用向量空间。
	SearchActiveChunks(ctx context.Context, query SearchQuery) ([]SearchResult, error)
}
