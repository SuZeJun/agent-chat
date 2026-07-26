package knowledgepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domain "agent-chat/internal/domain/knowledge"

	"github.com/jackc/pgx/v5"
)

// CreateFAQImport 原子创建 FAQ 文档、首版本和索引任务，并按规范化内容幂等。
func (repository *Repository) CreateFAQImport(
	ctx context.Context,
	knowledgeImport domain.FAQImport,
) (domain.CreateFAQImportResult, error) {
	if err := knowledgeImport.Validate(); err != nil {
		return domain.CreateFAQImportResult{}, fmt.Errorf("create FAQ import: %w", err)
	}

	transaction, err := repository.database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.CreateFAQImportResult{}, mapDatabaseError("create FAQ import", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	var persistedImportID string
	err = transaction.QueryRow(ctx, `
		INSERT INTO knowledge_imports (
			id,
			knowledge_base_id,
			source_name,
			content_sha256,
			total_rows,
			created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (knowledge_base_id, content_sha256) DO NOTHING
		RETURNING id
	`,
		knowledgeImport.ID,
		knowledgeImport.KnowledgeBaseID,
		knowledgeImport.SourceName,
		knowledgeImport.ContentSHA256,
		len(knowledgeImport.Items),
		knowledgeImport.CreatedAt,
	).Scan(&persistedImportID)
	duplicate := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !duplicate {
		return domain.CreateFAQImportResult{}, mapDatabaseError("create FAQ import", err)
	}
	if duplicate {
		err = transaction.QueryRow(ctx, `
			SELECT id
			FROM knowledge_imports
			WHERE knowledge_base_id = $1
			  AND content_sha256 = $2
		`, knowledgeImport.KnowledgeBaseID, knowledgeImport.ContentSHA256).
			Scan(&persistedImportID)
		if err != nil {
			return domain.CreateFAQImportResult{}, mapDatabaseError(
				"load duplicate FAQ import",
				err,
			)
		}
	} else {
		for _, item := range knowledgeImport.Items {
			if err := createFAQImportItem(ctx, transaction, persistedImportID, item); err != nil {
				return domain.CreateFAQImportResult{}, err
			}
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.CreateFAQImportResult{}, mapDatabaseError("create FAQ import", err)
	}

	snapshot, err := repository.LoadFAQImport(
		ctx,
		knowledgeImport.KnowledgeBaseID,
		persistedImportID,
	)
	if err != nil {
		return domain.CreateFAQImportResult{}, err
	}
	return domain.CreateFAQImportResult{
		Snapshot:  snapshot,
		Duplicate: duplicate,
	}, nil
}

// LoadFAQImport 读取指定知识库范围内的导入和逐行索引状态。
func (repository *Repository) LoadFAQImport(
	ctx context.Context,
	knowledgeBaseID string,
	importID string,
) (domain.FAQImportSnapshot, error) {
	var snapshot domain.FAQImportSnapshot
	err := repository.database.QueryRow(ctx, `
		SELECT
			id,
			knowledge_base_id,
			source_name,
			content_sha256,
			total_rows,
			created_at
		FROM knowledge_imports
		WHERE id = $1
		  AND knowledge_base_id = $2
	`, importID, knowledgeBaseID).Scan(
		&snapshot.ID,
		&snapshot.KnowledgeBaseID,
		&snapshot.SourceName,
		&snapshot.ContentSHA256,
		&snapshot.TotalRows,
		&snapshot.CreatedAt,
	)
	if err != nil {
		return domain.FAQImportSnapshot{}, mapDatabaseError("load FAQ import", err)
	}

	rows, err := repository.database.Query(ctx, `
		SELECT
			item.row_number,
			item.document_id,
			item.version_id,
			CASE
				WHEN version.index_status = 'ready' THEN 'ready'
				WHEN job.status = 'failed' THEN 'failed'
				WHEN job.status IN ('running', 'retry_wait')
				  OR version.index_status IN ('indexing', 'failed')
					THEN 'indexing'
				ELSE 'pending'
			END,
			CASE
				WHEN job.status = 'failed' THEN job.last_error
				ELSE ''
			END
		FROM knowledge_import_items AS item
		JOIN knowledge_document_versions AS version
		  ON version.id = item.version_id
		JOIN jobs AS job
		  ON job.id = item.job_id
		WHERE item.import_id = $1
		ORDER BY item.row_number
	`, importID)
	if err != nil {
		return domain.FAQImportSnapshot{}, mapDatabaseError("load FAQ import items", err)
	}
	defer rows.Close()

	snapshot.Items = make([]domain.FAQImportItemStatus, 0, snapshot.TotalRows)
	for rows.Next() {
		var item domain.FAQImportItemStatus
		if err := rows.Scan(
			&item.RowNumber,
			&item.DocumentID,
			&item.VersionID,
			&item.Status,
			&item.ErrorCode,
		); err != nil {
			return domain.FAQImportSnapshot{}, mapDatabaseError(
				"load FAQ import item",
				err,
			)
		}
		switch item.Status {
		case domain.IndexStatusReady:
			snapshot.ReadyRows++
		case domain.IndexStatusFailed:
			snapshot.FailedRows++
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.FAQImportSnapshot{}, mapDatabaseError("load FAQ import items", err)
	}
	if len(snapshot.Items) != snapshot.TotalRows {
		return domain.FAQImportSnapshot{}, errors.New(
			"load FAQ import: persisted row count is inconsistent",
		)
	}
	snapshot.Status = deriveFAQImportStatus(snapshot)
	return snapshot, nil
}

func createFAQImportItem(
	ctx context.Context,
	transaction pgx.Tx,
	importID string,
	item domain.FAQImportItem,
) error {
	metadata, err := encodeMetadata(item.Document.Metadata)
	if err != nil {
		return fmt.Errorf("create FAQ import item: %w", err)
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO knowledge_documents (
			id,
			knowledge_base_id,
			document_type,
			title,
			metadata
		)
		VALUES ($1, $2, $3, $4, $5)
	`,
		item.Document.ID,
		item.Document.KnowledgeBaseID,
		item.Document.Type,
		item.Document.Title,
		metadata,
	)
	if err != nil {
		return mapDatabaseError("create FAQ import document", err)
	}
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
		item.Version.ID,
		item.Version.DocumentID,
		item.Version.Number,
		item.Version.Content,
		item.Version.ContentSHA256,
		item.Version.EmbeddingIdentity.Provider,
		item.Version.EmbeddingIdentity.Model,
		item.Version.EmbeddingIdentity.Dimensions,
	)
	if err != nil {
		return mapDatabaseError("create FAQ import version", err)
	}
	jobPayload, err := json.Marshal(map[string]string{"version_id": item.Version.ID})
	if err != nil {
		return errors.New("create FAQ import item: encode job payload")
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
	`, item.JobID, domain.IndexJobType, item.Version.ID, jobPayload)
	if err != nil {
		return mapDatabaseError("create FAQ import job", err)
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO knowledge_import_items (
			import_id,
			row_number,
			document_id,
			version_id,
			job_id
		)
		VALUES ($1, $2, $3, $4, $5)
	`,
		importID,
		item.RowNumber,
		item.Document.ID,
		item.Version.ID,
		item.JobID,
	)
	if err != nil {
		return mapDatabaseError("create FAQ import item", err)
	}
	return nil
}

func deriveFAQImportStatus(snapshot domain.FAQImportSnapshot) domain.IndexStatus {
	if snapshot.FailedRows > 0 {
		return domain.IndexStatusFailed
	}
	if snapshot.ReadyRows == snapshot.TotalRows {
		return domain.IndexStatusReady
	}
	for _, item := range snapshot.Items {
		if item.Status == domain.IndexStatusIndexing ||
			item.Status == domain.IndexStatusReady {
			return domain.IndexStatusIndexing
		}
	}
	return domain.IndexStatusPending
}
