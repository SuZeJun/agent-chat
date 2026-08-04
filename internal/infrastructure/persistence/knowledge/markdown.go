package knowledgepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-chat/internal/application/knowledgedocument"
	domain "agent-chat/internal/domain/knowledge"

	"github.com/jackc/pgx/v5"
)

// CreateMarkdownDocument 在同一事务内创建逻辑文档、首版本和索引 Job。
func (repository *Repository) CreateMarkdownDocument(
	ctx context.Context,
	command knowledgedocument.CreateCommand,
) (knowledgedocument.DocumentDetail, error) {
	if command.Document.Type != domain.DocumentTypeMarkdown || command.Version.Number != 1 ||
		command.Version.DocumentID != command.Document.ID {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown document: invalid aggregate")
	}
	if err := command.Document.Validate(); err != nil {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown document: %w", err)
	}
	if err := command.Version.Validate(); err != nil {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown document: %w", err)
	}
	metadata, err := encodeMetadata(command.Document.Metadata)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown document: %w", err)
	}
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown document", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	tag, err := transaction.Exec(ctx, `
		INSERT INTO knowledge_documents (id, knowledge_base_id, document_type, title, metadata)
		SELECT $1, base.id, $3, $4, $5
		FROM knowledge_bases AS base
		WHERE base.id = $2
		  AND base.status = 'active'
	`, command.Document.ID, command.Document.KnowledgeBaseID, command.Document.Type,
		command.Document.Title, metadata)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown document", err)
	}
	if tag.RowsAffected() != 1 {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown document: %w", domain.ErrNotFound)
	}
	if err := insertMarkdownVersionAndJob(ctx, transaction, command.Version, command.JobID); err != nil {
		return knowledgedocument.DocumentDetail{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown document", err)
	}
	return repository.LoadMarkdownDocument(
		ctx, command.Document.KnowledgeBaseID, command.Document.ID,
	)
}

// CreateMarkdownVersion 锁定逻辑文档后分配单调版本号并原子创建索引 Job。
func (repository *Repository) CreateMarkdownVersion(
	ctx context.Context,
	command knowledgedocument.CreateVersionCommand,
) (knowledgedocument.DocumentDetail, error) {
	if strings.TrimSpace(command.KnowledgeBaseID) == "" || strings.TrimSpace(command.DocumentID) == "" ||
		strings.TrimSpace(command.VersionID) == "" || strings.TrimSpace(command.JobID) == "" {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown version: invalid identifiers")
	}
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown version", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var documentID string
	err = transaction.QueryRow(ctx, `
		SELECT document.id
		FROM knowledge_documents AS document
		JOIN knowledge_bases AS base ON base.id = document.knowledge_base_id
		WHERE document.id = $1
		  AND document.knowledge_base_id = $2
		  AND document.document_type = 'markdown'
		  AND document.deleted_at IS NULL
		  AND base.status = 'active'
		FOR UPDATE OF document
	`, command.DocumentID, command.KnowledgeBaseID).Scan(&documentID)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown version", err)
	}
	var latestNumber int
	var latestChecksum string
	err = transaction.QueryRow(ctx, `
		SELECT version, content_sha256
		FROM knowledge_document_versions
		WHERE document_id = $1
		ORDER BY version DESC
		LIMIT 1
	`, command.DocumentID).Scan(&latestNumber, &latestChecksum)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown version", err)
	}
	if latestChecksum == command.ContentSHA256 {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown version: %w", domain.ErrConflict)
	}
	version := domain.Version{
		ID: command.VersionID, DocumentID: command.DocumentID, Number: latestNumber + 1,
		Content: command.Content, ContentSHA256: command.ContentSHA256,
		EmbeddingIdentity: command.EmbeddingIdentity,
	}
	if err := version.Validate(); err != nil {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("create Markdown version: %w", err)
	}
	if err := insertMarkdownVersionAndJob(ctx, transaction, version, command.JobID); err != nil {
		return knowledgedocument.DocumentDetail{}, err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE knowledge_documents SET updated_at = now() WHERE id = $1
	`, command.DocumentID); err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown version", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("create Markdown version", err)
	}
	return repository.LoadMarkdownDocument(ctx, command.KnowledgeBaseID, command.DocumentID)
}

// ListMarkdownDocuments 返回指定知识库下未删除的 Markdown 文档和版本状态。
func (repository *Repository) ListMarkdownDocuments(
	ctx context.Context,
	knowledgeBaseID string,
) ([]knowledgedocument.DocumentItem, error) {
	details, err := repository.loadMarkdownDocuments(ctx, knowledgeBaseID, "", false)
	if err != nil {
		return nil, err
	}
	items := make([]knowledgedocument.DocumentItem, len(details))
	for index := range details {
		items[index] = details[index].DocumentItem
	}
	return items, nil
}

// LoadMarkdownDocument 返回指定知识库范围内的文档、版本和最新源内容。
func (repository *Repository) LoadMarkdownDocument(
	ctx context.Context,
	knowledgeBaseID string,
	documentID string,
) (knowledgedocument.DocumentDetail, error) {
	details, err := repository.loadMarkdownDocuments(ctx, knowledgeBaseID, documentID, true)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, err
	}
	if len(details) != 1 {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("load Markdown document: %w", domain.ErrNotFound)
	}
	return details[0], nil
}

// RetryMarkdownVersion 重置同一版本已失败的 Job，保留稳定幂等键。
func (repository *Repository) RetryMarkdownVersion(
	ctx context.Context,
	knowledgeBaseID string,
	documentID string,
	versionID string,
) (knowledgedocument.DocumentDetail, error) {
	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("retry Markdown version", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var versionStatus domain.IndexStatus
	var jobStatus string
	err = transaction.QueryRow(ctx, `
		SELECT version.index_status, job.status
		FROM knowledge_documents AS document
		JOIN knowledge_bases AS base
		  ON base.id = document.knowledge_base_id
		 AND base.status = 'active'
		JOIN knowledge_document_versions AS version ON version.document_id = document.id
		JOIN jobs AS job
		  ON job.job_type = $4
		 AND job.idempotency_key = version.id
		WHERE document.knowledge_base_id = $1
		  AND document.id = $2
		  AND document.document_type = 'markdown'
		  AND document.deleted_at IS NULL
		  AND version.id = $3
		FOR UPDATE OF document, version, job
	`, knowledgeBaseID, documentID, versionID, domain.IndexJobType).Scan(
		&versionStatus, &jobStatus,
	)
	if err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("retry Markdown version", err)
	}
	if jobStatus != "failed" ||
		(versionStatus != domain.IndexStatusFailed && versionStatus != domain.IndexStatusReady) {
		return knowledgedocument.DocumentDetail{}, fmt.Errorf("retry Markdown version: %w", domain.ErrInvalidState)
	}
	if versionStatus == domain.IndexStatusFailed {
		if _, err := transaction.Exec(ctx, `
			UPDATE knowledge_document_versions
			SET index_status = 'pending', index_error = '', indexed_at = NULL
			WHERE id = $1
		`, versionID); err != nil {
			return knowledgedocument.DocumentDetail{}, mapDatabaseError("retry Markdown version", err)
		}
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', attempts = 0, available_at = now(),
		    locked_at = NULL, locked_by = '', last_error = '', updated_at = now()
		WHERE job_type = $2 AND idempotency_key = $1
	`, versionID, domain.IndexJobType); err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("retry Markdown version", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return knowledgedocument.DocumentDetail{}, mapDatabaseError("retry Markdown version", err)
	}
	return repository.LoadMarkdownDocument(ctx, knowledgeBaseID, documentID)
}

func insertMarkdownVersionAndJob(
	ctx context.Context,
	transaction pgx.Tx,
	version domain.Version,
	jobID string,
) error {
	if strings.TrimSpace(jobID) == "" || len(jobID) > 64 {
		return fmt.Errorf("create Markdown index job: invalid job ID")
	}
	_, err := transaction.Exec(ctx, `
		INSERT INTO knowledge_document_versions (
			id, document_id, version, content, content_sha256, index_status,
			embedding_provider, embedding_model, embedding_dimensions
		)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8)
	`, version.ID, version.DocumentID, version.Number, version.Content, version.ContentSHA256,
		version.EmbeddingIdentity.Provider, version.EmbeddingIdentity.Model,
		version.EmbeddingIdentity.Dimensions)
	if err != nil {
		return mapDatabaseError("create Markdown version", err)
	}
	payload, err := json.Marshal(map[string]string{"version_id": version.ID})
	if err != nil {
		return errors.New("create Markdown index job: encode payload")
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO jobs (id, job_type, idempotency_key, payload, status)
		VALUES ($1, $2, $3, $4, 'pending')
	`, jobID, domain.IndexJobType, version.ID, payload)
	return mapDatabaseError("create Markdown index job", err)
}

func (repository *Repository) loadMarkdownDocuments(
	ctx context.Context,
	knowledgeBaseID string,
	documentID string,
	includeLatestContent bool,
) ([]knowledgedocument.DocumentDetail, error) {
	rows, err := repository.database.Query(ctx, `
		SELECT document.id, document.knowledge_base_id, document.title, document.metadata,
		       COALESCE(document.active_version_id, ''), document.created_at, document.updated_at
		FROM knowledge_documents AS document
		WHERE document.knowledge_base_id = $1
		  AND document.document_type = 'markdown'
		  AND document.deleted_at IS NULL
		  AND ($2 = '' OR document.id = $2)
		ORDER BY document.updated_at DESC, document.id
	`, knowledgeBaseID, documentID)
	if err != nil {
		return nil, mapDatabaseError("load Markdown documents", err)
	}
	defer rows.Close()
	details := make([]knowledgedocument.DocumentDetail, 0)
	indexByID := make(map[string]int)
	for rows.Next() {
		var detail knowledgedocument.DocumentDetail
		var metadata []byte
		if err := rows.Scan(&detail.ID, &detail.KnowledgeBaseID, &detail.Title, &metadata,
			&detail.ActiveVersionID, &detail.CreatedAt, &detail.UpdatedAt); err != nil {
			return nil, mapDatabaseError("load Markdown documents", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(metadata, &decoded); err != nil {
			return nil, errors.New("load Markdown documents: invalid persisted metadata")
		}
		if sourceURL, ok := decoded["source_url"].(string); ok {
			detail.SourceURL = sourceURL
		}
		detail.Versions = []knowledgedocument.VersionItem{}
		indexByID[detail.ID] = len(details)
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("load Markdown documents", err)
	}
	if len(details) == 0 {
		return details, nil
	}

	versionRows, err := repository.database.Query(ctx, `
		SELECT version.document_id, version.id, version.version,
		       CASE
		         WHEN job.status = 'failed' THEN 'failed'
		         WHEN job.status IN ('running', 'retry_wait')
		           OR version.index_status = 'indexing' THEN 'indexing'
		         WHEN job.status = 'pending' THEN 'pending'
		         WHEN version.index_status = 'ready' THEN 'ready'
		         WHEN version.index_status = 'failed' THEN 'failed'
		         ELSE 'pending'
		       END,
		       CASE
		         WHEN job.status = 'failed' THEN job.last_error
		         WHEN version.index_status = 'failed' THEN version.index_error
		         ELSE ''
		       END,
		       COALESCE(document.active_version_id = version.id, false),
		       version.created_at, version.indexed_at,
		       CASE WHEN $3 AND version.version = latest.latest_version THEN version.content ELSE '' END
		FROM knowledge_documents AS document
		JOIN knowledge_document_versions AS version ON version.document_id = document.id
		JOIN jobs AS job ON job.job_type = $4 AND job.idempotency_key = version.id
		JOIN LATERAL (
			SELECT max(candidate.version) AS latest_version
			FROM knowledge_document_versions AS candidate
			WHERE candidate.document_id = document.id
		) AS latest ON true
		WHERE document.knowledge_base_id = $1
		  AND document.document_type = 'markdown'
		  AND document.deleted_at IS NULL
		  AND ($2 = '' OR document.id = $2)
		ORDER BY document.id, version.version DESC
	`, knowledgeBaseID, documentID, includeLatestContent, domain.IndexJobType)
	if err != nil {
		return nil, mapDatabaseError("load Markdown versions", err)
	}
	defer versionRows.Close()
	for versionRows.Next() {
		var ownerID string
		var version knowledgedocument.VersionItem
		var latestContent string
		if err := versionRows.Scan(&ownerID, &version.ID, &version.Number, &version.Status,
			&version.ErrorCode, &version.Active, &version.CreatedAt, &version.IndexedAt,
			&latestContent); err != nil {
			return nil, mapDatabaseError("load Markdown versions", err)
		}
		index, ok := indexByID[ownerID]
		if !ok {
			return nil, errors.New("load Markdown versions: document scope is inconsistent")
		}
		details[index].Versions = append(details[index].Versions, version)
		if version.Number > details[index].LatestVersion {
			details[index].LatestVersion = version.Number
		}
		if latestContent != "" {
			details[index].LatestContent = latestContent
		}
	}
	if err := versionRows.Err(); err != nil {
		return nil, mapDatabaseError("load Markdown versions", err)
	}
	return details, nil
}
