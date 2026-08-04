package knowledgepg

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"agent-chat/internal/application/knowledgedocument"
	domain "agent-chat/internal/domain/knowledge"
)

func TestMarkdownDocumentLifecycleAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openKnowledgeTestDatabase(t, ctx, databaseURL)
	defer pool.Close()
	repository := NewRepository(pool)
	identity := domain.EmbeddingIdentity{Provider: "zhipu", Model: "embedding-3", Dimensions: 1024}
	base := domain.Base{ID: "base-markdown", Name: "文档知识库", Status: domain.BaseStatusActive}
	otherBase := domain.Base{ID: "base-other", Name: "其他知识库", Status: domain.BaseStatusActive}
	if err := repository.CreateBase(ctx, base); err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := repository.CreateBase(ctx, otherBase); err != nil {
		t.Fatalf("create other base: %v", err)
	}
	document := domain.Document{
		ID: "doc-markdown", KnowledgeBaseID: base.ID, Type: domain.DocumentTypeMarkdown,
		Title: "API 接入指南", Metadata: map[string]any{"source_url": "https://docs.example.com/api"},
	}
	versionOne := newTestVersion("ver-markdown-1", document.ID, 1, "# API\n\n第一版", identity)
	detail, err := repository.CreateMarkdownDocument(ctx, knowledgedocument.CreateCommand{
		Document: document, Version: versionOne, JobID: "job-markdown-1",
	})
	if err != nil {
		t.Fatalf("create Markdown document: %v", err)
	}
	if detail.LatestContent != versionOne.Content || len(detail.Versions) != 1 ||
		detail.Versions[0].Status != domain.IndexStatusPending {
		t.Fatalf("unexpected created detail: %#v", detail)
	}
	assertIndexJob(t, ctx, pool, "job-markdown-1", versionOne.ID)
	rollbackDocument := domain.Document{
		ID: "doc-markdown-rollback", KnowledgeBaseID: base.ID,
		Type: domain.DocumentTypeMarkdown, Title: "应回滚的文档", Metadata: map[string]any{},
	}
	rollbackVersion := newTestVersion(
		"ver-markdown-rollback", rollbackDocument.ID, 1, "# 应回滚", identity,
	)
	if _, err := repository.CreateMarkdownDocument(ctx, knowledgedocument.CreateCommand{
		Document: rollbackDocument, Version: rollbackVersion,
		// 复用已存在的 Job ID 强制最后一步失败，验证前两步不会泄漏提交。
		JobID: "job-markdown-1",
	}); err == nil {
		t.Fatal("expected duplicate job ID to roll back aggregate")
	}
	var rolledBackDocuments int
	var rolledBackVersions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents WHERE id = $1`,
		rollbackDocument.ID).Scan(&rolledBackDocuments); err != nil {
		t.Fatalf("count rolled back document: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_document_versions WHERE id = $1`,
		rollbackVersion.ID).Scan(&rolledBackVersions); err != nil {
		t.Fatalf("count rolled back version: %v", err)
	}
	if rolledBackDocuments != 0 || rolledBackVersions != 0 {
		t.Fatalf("partial aggregate escaped rollback: documents=%d versions=%d",
			rolledBackDocuments, rolledBackVersions)
	}
	if _, err := repository.LoadMarkdownDocument(ctx, otherBase.ID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected scoped not found, got %v", err)
	}

	chunk := domain.Chunk{ID: "chunk-markdown-1", VersionID: versionOne.ID, Position: 0,
		Content: versionOne.Content, Metadata: map[string]any{"kind": "markdown"}, Embedding: testVector(0)}
	if err := repository.ReplaceChunksAndMarkReady(ctx, versionOne.ID, identity, []domain.Chunk{chunk}, time.Now()); err != nil {
		t.Fatalf("index first version: %v", err)
	}
	if err := repository.PublishVersion(ctx, document.ID, versionOne.ID, identity, time.Now()); err != nil {
		t.Fatalf("publish first version: %v", err)
	}

	detail, err = repository.CreateMarkdownVersion(ctx, knowledgedocument.CreateVersionCommand{
		KnowledgeBaseID: base.ID, DocumentID: document.ID, VersionID: "ver-markdown-2",
		Content: "# API\n\n第二版", ContentSHA256: domain.ContentChecksum("# API\n\n第二版"),
		EmbeddingIdentity: identity, JobID: "job-markdown-2",
	})
	if err != nil {
		t.Fatalf("create second version: %v", err)
	}
	if detail.ActiveVersionID != versionOne.ID || detail.LatestVersion != 2 ||
		detail.LatestContent != "# API\n\n第二版" {
		t.Fatalf("old version did not remain active: %#v", detail)
	}
	_, err = repository.CreateMarkdownVersion(ctx, knowledgedocument.CreateVersionCommand{
		KnowledgeBaseID: base.ID, DocumentID: document.ID, VersionID: "ver-duplicate",
		Content: "# API\n\n第二版", ContentSHA256: domain.ContentChecksum("# API\n\n第二版"),
		EmbeddingIdentity: identity, JobID: "job-duplicate",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected unchanged content conflict, got %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'failed', index_error = 'embedding_failed'
		WHERE id = 'ver-markdown-2';
		UPDATE jobs
		SET status = 'failed', attempts = max_attempts, last_error = 'embedding_failed'
		WHERE id = 'job-markdown-2'
	`); err != nil {
		t.Fatalf("seed failed version: %v", err)
	}
	detail, err = repository.RetryMarkdownVersion(ctx, base.ID, document.ID, "ver-markdown-2")
	if err != nil {
		t.Fatalf("retry failed version: %v", err)
	}
	if detail.Versions[0].Status != domain.IndexStatusPending || detail.Versions[0].ErrorCode != "" {
		t.Fatalf("unexpected retried version: %#v", detail.Versions[0])
	}
	assertIndexJob(t, ctx, pool, "job-markdown-2", "ver-markdown-2")

	// embedding 已完成但发布失败时，Job 失败必须优先暴露，否则管理员无法重试。
	if _, err := pool.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'ready', index_error = '', indexed_at = now()
		WHERE id = 'ver-markdown-2';
		UPDATE jobs
		SET status = 'failed', attempts = max_attempts, last_error = 'publish_failed'
		WHERE id = 'job-markdown-2'
	`); err != nil {
		t.Fatalf("seed failed publish: %v", err)
	}
	detail, err = repository.LoadMarkdownDocument(ctx, base.ID, document.ID)
	if err != nil {
		t.Fatalf("load failed publish: %v", err)
	}
	if detail.Versions[0].Status != domain.IndexStatusFailed || detail.Versions[0].ErrorCode != "publish_failed" {
		t.Fatalf("failed publish was hidden: %#v", detail.Versions[0])
	}
	detail, err = repository.RetryMarkdownVersion(ctx, base.ID, document.ID, "ver-markdown-2")
	if err != nil {
		t.Fatalf("retry failed publish: %v", err)
	}
	if detail.Versions[0].Status != domain.IndexStatusPending || detail.Versions[0].Active {
		t.Fatalf("retried publish was reported as ready: %#v", detail.Versions[0])
	}
	assertIndexJob(t, ctx, pool, "job-markdown-2", "ver-markdown-2")
	var versionJobCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE job_type = $1 AND idempotency_key = $2
	`, domain.IndexJobType, "ver-markdown-2").Scan(&versionJobCount); err != nil {
		t.Fatalf("count retried jobs: %v", err)
	}
	if versionJobCount != 1 {
		t.Fatalf("retry created duplicate jobs: %d", versionJobCount)
	}
	if _, err := pool.Exec(ctx, `UPDATE knowledge_bases SET status = 'disabled' WHERE id = $1`, base.ID); err != nil {
		t.Fatalf("disable base: %v", err)
	}
	if _, err := repository.RetryMarkdownVersion(ctx, base.ID, document.ID, versionOne.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected disabled base retry to be rejected, got %v", err)
	}
}
