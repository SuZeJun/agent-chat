package knowledgepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	domain "agent-chat/internal/domain/knowledge"
	"agent-chat/internal/infrastructure/persistence"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryVersionLifecycleAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openKnowledgeTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := NewRepository(pool)
	identity := domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	base := domain.Base{
		ID:          "base-1",
		Name:        "产品帮助中心",
		Description: "FAQ RAG 集成测试",
		Status:      domain.BaseStatusActive,
	}
	document := domain.Document{
		ID:              "document-1",
		KnowledgeBaseID: base.ID,
		Type:            domain.DocumentTypeFAQ,
		Title:           "如何重置密码？",
		Metadata:        map[string]any{"locale": "zh-CN"},
	}
	if err := repository.CreateBase(ctx, base); err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := repository.CreateDocument(ctx, document); err != nil {
		t.Fatalf("create document: %v", err)
	}

	versionOne := newTestVersion("version-1", document.ID, 1, "请在安全设置中重置密码。", identity)
	if err := repository.CreateVersionAndIndexJob(ctx, versionOne, "job-1"); err != nil {
		t.Fatalf("create first version: %v", err)
	}
	assertIndexJob(t, ctx, pool, "job-1", versionOne.ID)

	err := repository.PublishVersion(ctx, document.ID, versionOne.ID, identity, time.Now())
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("expected pending version publish to fail, got %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE knowledge_documents
		SET active_version_id = $2
		WHERE id = $1
	`, document.ID, versionOne.ID); err == nil {
		t.Fatal("expected database trigger to reject pending active version")
	}

	vectorOne := testVector(0)
	chunkOne := domain.Chunk{
		ID:         "chunk-1",
		VersionID:  versionOne.ID,
		Position:   0,
		Content:    versionOne.Content,
		TokenCount: 12,
		Metadata:   map[string]any{"locale": "zh-CN", "kind": "faq"},
		Embedding:  vectorOne,
	}
	if err := repository.ReplaceChunksAndMarkReady(
		ctx,
		versionOne.ID,
		identity,
		[]domain.Chunk{chunkOne},
		time.Now(),
	); err != nil {
		t.Fatalf("index first version: %v", err)
	}
	if err := repository.PublishVersion(
		ctx,
		document.ID,
		versionOne.ID,
		identity,
		time.Now(),
	); err != nil {
		t.Fatalf("publish first version: %v", err)
	}
	results := searchChunks(t, ctx, repository, base.ID, identity, vectorOne)
	if len(results) != 1 || results[0].VersionID != versionOne.ID {
		t.Fatalf("unexpected first version results: %#v", results)
	}

	versionTwo := newTestVersion("version-2", document.ID, 2, "请使用登录页的忘记密码入口。", identity)
	if err := repository.CreateVersionAndIndexJob(ctx, versionTwo, "job-2"); err != nil {
		t.Fatalf("create second version: %v", err)
	}
	results = searchChunks(t, ctx, repository, base.ID, identity, vectorOne)
	if len(results) != 1 || results[0].VersionID != versionOne.ID {
		t.Fatalf("old active version stopped serving before publish: %#v", results)
	}

	vectorTwo := testVector(1)
	chunkTwo := domain.Chunk{
		ID:         "chunk-2",
		VersionID:  versionTwo.ID,
		Position:   0,
		Content:    versionTwo.Content,
		TokenCount: 14,
		Metadata:   map[string]any{"locale": "zh-CN", "kind": "faq"},
		Embedding:  vectorTwo,
	}
	if err := repository.ReplaceChunksAndMarkReady(
		ctx,
		versionTwo.ID,
		identity,
		[]domain.Chunk{chunkTwo},
		time.Now(),
	); err != nil {
		t.Fatalf("index second version: %v", err)
	}
	if err := repository.PublishVersion(
		ctx,
		document.ID,
		versionTwo.ID,
		identity,
		time.Now(),
	); err != nil {
		t.Fatalf("publish second version: %v", err)
	}
	results = searchChunks(t, ctx, repository, base.ID, identity, vectorTwo)
	if len(results) != 1 || results[0].VersionID != versionTwo.ID {
		t.Fatalf("old version still participated after publish: %#v", results)
	}

	mismatchedIdentity := identity
	mismatchedIdentity.Model = "another-model"
	_, err = repository.SearchActiveChunks(ctx, domain.SearchQuery{
		KnowledgeBaseID:   base.ID,
		EmbeddingIdentity: mismatchedIdentity,
		Embedding:         vectorTwo,
		Limit:             5,
		MinimumSimilarity: 0,
	})
	if !errors.Is(err, domain.ErrEmbeddingIdentityMismatch) {
		t.Fatalf("expected embedding identity mismatch, got %v", err)
	}

	versionThree := newTestVersion("version-3", document.ID, 3, "不会提交的版本", identity)
	err = repository.CreateVersionAndIndexJob(ctx, versionThree, "job-1")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected duplicate job conflict, got %v", err)
	}
	var versionThreeExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM knowledge_document_versions
			WHERE id = $1
		)
	`, versionThree.ID).Scan(&versionThreeExists); err != nil {
		t.Fatalf("check rolled back version: %v", err)
	}
	if versionThreeExists {
		t.Fatal("version insert was not rolled back with job conflict")
	}

	var hnswIndexCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname = 'knowledge_chunks_embedding_hnsw_index'
	`).Scan(&hnswIndexCount); err != nil {
		t.Fatalf("check HNSW index: %v", err)
	}
	if hnswIndexCount != 1 {
		t.Fatalf("unexpected HNSW index count: %d", hnswIndexCount)
	}
}

func openKnowledgeTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	schemaName := fmt.Sprintf("knowledge_repository_test_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA "+schemaIdentifier+" CASCADE")
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := persistence.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

func newTestVersion(
	id string,
	documentID string,
	number int,
	content string,
	identity domain.EmbeddingIdentity,
) domain.Version {
	return domain.Version{
		ID:                id,
		DocumentID:        documentID,
		Number:            number,
		Content:           content,
		ContentSHA256:     domain.ContentChecksum(content),
		EmbeddingIdentity: identity,
	}
}

func testVector(axis int) []float64 {
	vector := make([]float64, 1024)
	vector[axis] = 1
	return vector
}

func searchChunks(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	baseID string,
	identity domain.EmbeddingIdentity,
	vector []float64,
) []domain.SearchResult {
	t.Helper()

	results, err := repository.SearchActiveChunks(ctx, domain.SearchQuery{
		KnowledgeBaseID:   baseID,
		EmbeddingIdentity: identity,
		Embedding:         vector,
		Metadata:          map[string]any{"locale": "zh-CN"},
		Limit:             5,
		MinimumSimilarity: 0.5,
	})
	if err != nil {
		t.Fatalf("search active chunks: %v", err)
	}
	return results
}

func assertIndexJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID string,
	versionID string,
) {
	t.Helper()

	var jobType string
	var idempotencyKey string
	var status string
	var rawPayload []byte
	if err := pool.QueryRow(ctx, `
		SELECT job_type, idempotency_key, status, payload
		FROM jobs
		WHERE id = $1
	`, jobID).Scan(&jobType, &idempotencyKey, &status, &rawPayload); err != nil {
		t.Fatalf("load index job: %v", err)
	}
	if jobType != domain.IndexJobType || idempotencyKey != versionID || status != "pending" {
		t.Fatalf(
			"unexpected index job: type=%s idempotency=%s status=%s",
			jobType,
			idempotencyKey,
			status,
		)
	}
	var payload map[string]string
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("decode index job payload: %v", err)
	}
	if payload["version_id"] != versionID {
		t.Fatalf("unexpected index job payload: %#v", payload)
	}
}
