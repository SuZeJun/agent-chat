package knowledgepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	domain "agent-chat/internal/domain/knowledge"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxIndexErrorLength = 2000

var _ domain.Repository = (*Repository)(nil)

// Repository 使用 PostgreSQL、pgvector 和现有 jobs 表持久化知识索引生命周期。
type Repository struct {
	database *pgxpool.Pool
}

// NewRepository 创建知识 PostgreSQL Repository。
func NewRepository(database *pgxpool.Pool) *Repository {
	return &Repository{database: database}
}

// CreateBase 创建知识库。
func (repository *Repository) CreateBase(ctx context.Context, base domain.Base) error {
	if err := base.Validate(); err != nil {
		return fmt.Errorf("create knowledge base: %w", err)
	}
	_, err := repository.database.Exec(ctx, `
		INSERT INTO knowledge_bases (id, name, description, status)
		VALUES ($1, $2, $3, $4)
	`, base.ID, base.Name, base.Description, base.Status)
	return mapDatabaseError("create knowledge base", err)
}

// CreateDocument 创建尚未发布版本的逻辑文档。
func (repository *Repository) CreateDocument(ctx context.Context, document domain.Document) error {
	if err := document.Validate(); err != nil {
		return fmt.Errorf("create knowledge document: %w", err)
	}
	metadata, err := encodeMetadata(document.Metadata)
	if err != nil {
		return fmt.Errorf("create knowledge document: %w", err)
	}
	_, err = repository.database.Exec(ctx, `
		INSERT INTO knowledge_documents (
			id,
			knowledge_base_id,
			document_type,
			title,
			metadata
		)
		VALUES ($1, $2, $3, $4, $5)
	`, document.ID, document.KnowledgeBaseID, document.Type, document.Title, metadata)
	return mapDatabaseError("create knowledge document", err)
}

// CreateVersionAndIndexJob 原子创建不可变版本和持久化索引任务。
func (repository *Repository) CreateVersionAndIndexJob(
	ctx context.Context,
	version domain.Version,
	jobID string,
) error {
	if err := version.Validate(); err != nil {
		return fmt.Errorf("create knowledge version: %w", err)
	}
	if strings.TrimSpace(jobID) == "" || len(jobID) > 64 {
		return fmt.Errorf("create knowledge version: job ID must be 1-64 characters")
	}
	jobPayload, err := json.Marshal(map[string]string{"version_id": version.ID})
	if err != nil {
		return fmt.Errorf("create knowledge version: encode index job payload")
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("create knowledge version", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	_, err = transaction.Exec(ctx, `
		INSERT INTO knowledge_document_versions (
			id,
			document_id,
			version,
			content,
			content_sha256,
			index_status,
			embedding_provider,
			embedding_model,
			embedding_dimensions
		)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8)
	`,
		version.ID,
		version.DocumentID,
		version.Number,
		version.Content,
		version.ContentSHA256,
		version.EmbeddingIdentity.Provider,
		version.EmbeddingIdentity.Model,
		version.EmbeddingIdentity.Dimensions,
	)
	if err != nil {
		return mapDatabaseError("create knowledge version", err)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO jobs (
			id,
			job_type,
			idempotency_key,
			payload,
			status
		)
		VALUES ($1, $2, $3, $4, 'pending')
	`, jobID, domain.IndexJobType, version.ID, jobPayload)
	if err != nil {
		return mapDatabaseError("create knowledge index job", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return mapDatabaseError("create knowledge version", err)
	}
	return nil
}

// LoadIndexSource 读取 Worker 构建索引所需的逻辑文档、版本和发布状态。
func (repository *Repository) LoadIndexSource(
	ctx context.Context,
	versionID string,
) (domain.IndexSource, error) {
	if strings.TrimSpace(versionID) == "" {
		return domain.IndexSource{}, fmt.Errorf("load knowledge index source: version ID must not be blank")
	}

	var source domain.IndexSource
	var documentType string
	var status string
	var metadata []byte
	err := repository.database.QueryRow(ctx, `
		SELECT
			document.id,
			document.knowledge_base_id,
			document.document_type,
			document.title,
			document.metadata,
			version.id,
			version.document_id,
			version.version,
			version.content,
			version.content_sha256,
			version.embedding_provider,
			version.embedding_model,
			version.embedding_dimensions,
			version.index_status,
			COALESCE(document.active_version_id = version.id, false)
		FROM knowledge_document_versions AS version
		JOIN knowledge_documents AS document
		  ON document.id = version.document_id
		WHERE version.id = $1
		  AND document.deleted_at IS NULL
	`, versionID).Scan(
		&source.Document.ID,
		&source.Document.KnowledgeBaseID,
		&documentType,
		&source.Document.Title,
		&metadata,
		&source.Version.ID,
		&source.Version.DocumentID,
		&source.Version.Number,
		&source.Version.Content,
		&source.Version.ContentSHA256,
		&source.Version.EmbeddingIdentity.Provider,
		&source.Version.EmbeddingIdentity.Model,
		&source.Version.EmbeddingIdentity.Dimensions,
		&status,
		&source.Active,
	)
	if err != nil {
		return domain.IndexSource{}, mapDatabaseError("load knowledge index source", err)
	}
	source.Document.Type = domain.DocumentType(documentType)
	source.Status = domain.IndexStatus(status)
	if err := json.Unmarshal(metadata, &source.Document.Metadata); err != nil {
		return domain.IndexSource{}, fmt.Errorf("load knowledge index source: invalid persisted metadata")
	}
	if err := source.Validate(); err != nil {
		return domain.IndexSource{}, fmt.Errorf("load knowledge index source: %w", err)
	}
	return source, nil
}

// ReplaceChunksAndMarkReady 原子替换未发布版本切片并将版本标记为 ready。
func (repository *Repository) ReplaceChunksAndMarkReady(
	ctx context.Context,
	versionID string,
	identity domain.EmbeddingIdentity,
	chunks []domain.Chunk,
	indexedAt time.Time,
) error {
	if strings.TrimSpace(versionID) == "" {
		return fmt.Errorf("replace knowledge chunks: version ID must not be blank")
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("replace knowledge chunks: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("replace knowledge chunks: at least one chunk is required")
	}
	if indexedAt.IsZero() {
		return fmt.Errorf("replace knowledge chunks: indexed time is required")
	}
	for _, chunk := range chunks {
		if chunk.VersionID != versionID {
			return fmt.Errorf("replace knowledge chunks: chunk version does not match target version")
		}
		if err := chunk.Validate(identity); err != nil {
			return fmt.Errorf("replace knowledge chunks: %w", err)
		}
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("replace knowledge chunks", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	snapshot, err := loadVersionForUpdate(ctx, transaction, versionID)
	if err != nil {
		return err
	}
	if !snapshot.identity.Equal(identity) {
		return fmt.Errorf("replace knowledge chunks: %w", domain.ErrEmbeddingIdentityMismatch)
	}
	switch snapshot.status {
	case domain.IndexStatusPending, domain.IndexStatusIndexing, domain.IndexStatusFailed:
	default:
		return fmt.Errorf("replace knowledge chunks: %w", domain.ErrInvalidState)
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'indexing',
		    index_error = '',
		    indexed_at = NULL
		WHERE id = $1
	`, versionID); err != nil {
		return mapDatabaseError("mark knowledge version indexing", err)
	}
	if _, err := transaction.Exec(ctx, `
		DELETE FROM knowledge_chunks
		WHERE version_id = $1
	`, versionID); err != nil {
		return mapDatabaseError("replace knowledge chunks", err)
	}

	for _, chunk := range chunks {
		metadata, err := encodeMetadata(chunk.Metadata)
		if err != nil {
			return fmt.Errorf("replace knowledge chunks: %w", err)
		}
		vector, err := vectorLiteral(chunk.Embedding)
		if err != nil {
			return fmt.Errorf("replace knowledge chunks: %w", err)
		}
		_, err = transaction.Exec(ctx, `
			INSERT INTO knowledge_chunks (
				id,
				version_id,
				position,
				content,
				token_count,
				metadata,
				embedding
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7::vector)
		`,
			chunk.ID,
			chunk.VersionID,
			chunk.Position,
			chunk.Content,
			chunk.TokenCount,
			metadata,
			vector,
		)
		if err != nil {
			return mapDatabaseError("insert knowledge chunk", err)
		}
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'ready',
		    index_error = '',
		    indexed_at = $2
		WHERE id = $1
	`, versionID, indexedAt); err != nil {
		return mapDatabaseError("mark knowledge version ready", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return mapDatabaseError("replace knowledge chunks", err)
	}
	return nil
}

// MarkVersionFailed 记录可安全展示的索引失败原因。
func (repository *Repository) MarkVersionFailed(
	ctx context.Context,
	versionID string,
	reason string,
) error {
	reason = strings.TrimSpace(reason)
	if strings.TrimSpace(versionID) == "" {
		return fmt.Errorf("mark knowledge version failed: version ID must not be blank")
	}
	if reason == "" || len(reason) > maxIndexErrorLength {
		return fmt.Errorf(
			"mark knowledge version failed: reason must be 1-%d characters",
			maxIndexErrorLength,
		)
	}
	commandTag, err := repository.database.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'failed',
		    index_error = $2,
		    indexed_at = NULL
		WHERE id = $1
		  AND index_status IN ('pending', 'indexing', 'failed')
	`, versionID, reason)
	if err != nil {
		return mapDatabaseError("mark knowledge version failed", err)
	}
	if commandTag.RowsAffected() == 1 {
		return nil
	}

	var exists bool
	if err := repository.database.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM knowledge_document_versions
			WHERE id = $1
		)
	`, versionID).Scan(&exists); err != nil {
		return mapDatabaseError("mark knowledge version failed", err)
	}
	if !exists {
		return fmt.Errorf("mark knowledge version failed: %w", domain.ErrNotFound)
	}
	return fmt.Errorf("mark knowledge version failed: %w", domain.ErrInvalidState)
}

// PublishVersion 将 ready 版本原子切换为文档活动版本。
func (repository *Repository) PublishVersion(
	ctx context.Context,
	documentID string,
	versionID string,
	identity domain.EmbeddingIdentity,
	publishedAt time.Time,
) error {
	if strings.TrimSpace(documentID) == "" || strings.TrimSpace(versionID) == "" {
		return fmt.Errorf("publish knowledge version: document and version IDs are required")
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("publish knowledge version: %w", err)
	}
	if publishedAt.IsZero() {
		return fmt.Errorf("publish knowledge version: published time is required")
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapDatabaseError("publish knowledge version", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	var status string
	var provider string
	var model string
	var dimensions int
	var targetVersion int
	var activeVersionID *string
	var activeVersionNumber *int
	err = transaction.QueryRow(ctx, `
		SELECT
			version.index_status,
			version.embedding_provider,
			version.embedding_model,
			version.embedding_dimensions,
			version.version,
			document.active_version_id,
			active_version.version
		FROM knowledge_documents AS document
		JOIN knowledge_document_versions AS version
		  ON version.document_id = document.id
		 AND version.id = $2
		LEFT JOIN knowledge_document_versions AS active_version
		  ON active_version.id = document.active_version_id
		WHERE document.id = $1
		  AND document.deleted_at IS NULL
		FOR UPDATE OF document, version
	`, documentID, versionID).Scan(
		&status,
		&provider,
		&model,
		&dimensions,
		&targetVersion,
		&activeVersionID,
		&activeVersionNumber,
	)
	if err != nil {
		return mapDatabaseError("publish knowledge version", err)
	}
	if domain.IndexStatus(status) != domain.IndexStatusReady {
		return fmt.Errorf("publish knowledge version: %w", domain.ErrInvalidState)
	}
	versionIdentity := domain.EmbeddingIdentity{
		Provider:   provider,
		Model:      model,
		Dimensions: dimensions,
	}
	if !versionIdentity.Equal(identity) {
		return fmt.Errorf("publish knowledge version: %w", domain.ErrEmbeddingIdentityMismatch)
	}
	if activeVersionID != nil && *activeVersionID == versionID {
		if err := transaction.Commit(ctx); err != nil {
			return mapDatabaseError("publish knowledge version", err)
		}
		return nil
	}
	if activeVersionNumber != nil && *activeVersionNumber > targetVersion {
		return fmt.Errorf("publish knowledge version: %w", domain.ErrVersionSuperseded)
	}

	if _, err := transaction.Exec(ctx, `
		UPDATE knowledge_documents
		SET active_version_id = $2,
		    updated_at = $3
		WHERE id = $1
	`, documentID, versionID, publishedAt); err != nil {
		return mapDatabaseError("publish knowledge version", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return mapDatabaseError("publish knowledge version", err)
	}
	return nil
}

// SearchActiveChunks 仅检索当前活动版本，并拒绝混用向量空间。
func (repository *Repository) SearchActiveChunks(
	ctx context.Context,
	query domain.SearchQuery,
) ([]domain.SearchResult, error) {
	if err := query.Validate(); err != nil {
		return nil, fmt.Errorf("search active knowledge chunks: %w", err)
	}
	vector, err := vectorLiteral(query.Embedding)
	if err != nil {
		return nil, fmt.Errorf("search active knowledge chunks: %w", err)
	}
	metadata, err := encodeMetadata(query.Metadata)
	if err != nil {
		return nil, fmt.Errorf("search active knowledge chunks: %w", err)
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, mapDatabaseError("search active knowledge chunks", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	identities, err := transaction.Query(ctx, `
		SELECT DISTINCT
			version.embedding_provider,
			version.embedding_model,
			version.embedding_dimensions
		FROM knowledge_bases AS base
		JOIN knowledge_documents AS document
		  ON document.knowledge_base_id = base.id
		 AND document.deleted_at IS NULL
		 AND document.active_version_id IS NOT NULL
		JOIN knowledge_document_versions AS version
		  ON version.id = document.active_version_id
		WHERE base.id = $1
		  AND base.status = 'active'
	`, query.KnowledgeBaseID)
	if err != nil {
		return nil, mapDatabaseError("check active embedding identity", err)
	}
	for identities.Next() {
		var provider string
		var model string
		var dimensions int
		if err := identities.Scan(&provider, &model, &dimensions); err != nil {
			identities.Close()
			return nil, mapDatabaseError("check active embedding identity", err)
		}
		activeIdentity := domain.EmbeddingIdentity{
			Provider:   provider,
			Model:      model,
			Dimensions: dimensions,
		}
		if !activeIdentity.Equal(query.EmbeddingIdentity) {
			identities.Close()
			return nil, fmt.Errorf(
				"search active knowledge chunks: %w",
				domain.ErrEmbeddingIdentityMismatch,
			)
		}
	}
	if err := identities.Err(); err != nil {
		identities.Close()
		return nil, mapDatabaseError("check active embedding identity", err)
	}
	identities.Close()

	rows, err := transaction.Query(ctx, `
		SELECT
			chunk.id,
			document.id,
			version.id,
			document.document_type,
			document.title,
			chunk.content,
			chunk.metadata,
			GREATEST(
				-1.0,
				LEAST(1.0, 1 - (chunk.embedding <=> $2::vector))
			) AS similarity
		FROM knowledge_bases AS base
		JOIN knowledge_documents AS document
		  ON document.knowledge_base_id = base.id
		 AND document.deleted_at IS NULL
		JOIN knowledge_document_versions AS version
		  ON version.id = document.active_version_id
		 AND version.index_status = 'ready'
		JOIN knowledge_chunks AS chunk
		  ON chunk.version_id = version.id
		WHERE base.id = $1
		  AND base.status = 'active'
		  AND chunk.metadata @> $3::jsonb
		  AND 1 - (chunk.embedding <=> $2::vector) >= $4
		ORDER BY chunk.embedding <=> $2::vector, chunk.id
		LIMIT $5
	`, query.KnowledgeBaseID, vector, metadata, query.MinimumSimilarity, query.Limit)
	if err != nil {
		return nil, mapDatabaseError("search active knowledge chunks", err)
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, query.Limit)
	for rows.Next() {
		var result domain.SearchResult
		var documentType string
		var rawMetadata []byte
		if err := rows.Scan(
			&result.ChunkID,
			&result.DocumentID,
			&result.VersionID,
			&documentType,
			&result.Title,
			&result.Content,
			&rawMetadata,
			&result.Similarity,
		); err != nil {
			return nil, mapDatabaseError("search active knowledge chunks", err)
		}
		if err := json.Unmarshal(rawMetadata, &result.Metadata); err != nil {
			return nil, fmt.Errorf("search active knowledge chunks: decode metadata")
		}
		result.DocumentType = domain.DocumentType(documentType)
		result.Rank = len(results) + 1
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("search active knowledge chunks", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, mapDatabaseError("search active knowledge chunks", err)
	}
	return results, nil
}

type versionSnapshot struct {
	status   domain.IndexStatus
	identity domain.EmbeddingIdentity
}

func loadVersionForUpdate(
	ctx context.Context,
	transaction pgx.Tx,
	versionID string,
) (versionSnapshot, error) {
	var status string
	var provider string
	var model string
	var dimensions int
	err := transaction.QueryRow(ctx, `
		SELECT
			index_status,
			embedding_provider,
			embedding_model,
			embedding_dimensions
		FROM knowledge_document_versions
		WHERE id = $1
		FOR UPDATE
	`, versionID).Scan(&status, &provider, &model, &dimensions)
	if err != nil {
		return versionSnapshot{}, mapDatabaseError("load knowledge version", err)
	}
	return versionSnapshot{
		status: domain.IndexStatus(status),
		identity: domain.EmbeddingIdentity{
			Provider:   provider,
			Model:      model,
			Dimensions: dimensions,
		},
	}, nil
}

func encodeMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte(`{}`), nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("metadata must be valid JSON")
	}
	return encoded, nil
}

func vectorLiteral(vector []float64) (string, error) {
	if len(vector) != 1024 {
		return "", fmt.Errorf("vector must contain exactly 1024 dimensions")
	}
	var builder strings.Builder
	builder.Grow(len(vector)*4 + 2)
	builder.WriteByte('[')
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", fmt.Errorf("vector must contain only finite values")
		}
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	}
	builder.WriteByte(']')
	return builder.String(), nil
}

func mapDatabaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
		case "23503":
			return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
		case "23514":
			return fmt.Errorf("%s: %w", operation, domain.ErrInvalidState)
		}
	}
	// PostgreSQL 原始错误可能包含 SQL、Schema 或数据片段，不跨越 Infrastructure 边界。
	return fmt.Errorf("%s: database operation failed", operation)
}
