package knowledgeindex

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"agent-chat/internal/application/knowledgeretrieve"
	domain "agent-chat/internal/domain/knowledge"
	"agent-chat/internal/infrastructure/persistence"
	knowledgepg "agent-chat/internal/infrastructure/persistence/knowledge"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIndexerLifecycleAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIndexerTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := knowledgepg.NewRepository(pool)
	identity := domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	base := domain.Base{
		ID:     "base-1",
		Name:   "帮助中心",
		Status: domain.BaseStatusActive,
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

	versionOne := integrationVersion(
		"version-1",
		document.ID,
		1,
		"请在登录页点击忘记密码。",
		identity,
	)
	if err := repository.CreateVersionAndIndexJob(ctx, versionOne, "job-1"); err != nil {
		t.Fatalf("create first version: %v", err)
	}
	embedder := &fakeEmbedder{identity: identity}
	indexer := newTestIndexer(t, repository, embedder, NewDeterministicChunker())
	if err := indexer.IndexVersion(ctx, versionOne.ID); err != nil {
		t.Fatalf("index first version: %v", err)
	}
	source, err := repository.LoadIndexSource(ctx, versionOne.ID)
	if err != nil {
		t.Fatalf("load indexed source: %v", err)
	}
	if source.Status != domain.IndexStatusReady || !source.Active {
		t.Fatalf("unexpected indexed source: %#v", source)
	}
	retrievalService, err := knowledgeretrieve.NewService(repository, embedder)
	if err != nil {
		t.Fatalf("create retrieval service: %v", err)
	}
	results, err := retrievalService.Retrieve(ctx, knowledgeretrieve.Request{
		KnowledgeBaseID:   base.ID,
		Query:             "如何重置密码？",
		Metadata:          map[string]any{"locale": "zh-CN"},
		Limit:             5,
		MinimumSimilarity: 0.5,
	})
	if err != nil {
		t.Fatalf("search indexed chunks: %v", err)
	}
	if len(results) != 1 ||
		results[0].VersionID != versionOne.ID ||
		results[0].Content != "问题：如何重置密码？\n答案：请在登录页点击忘记密码。" {
		t.Fatalf("unexpected search results: %#v", results)
	}

	embeddingCalls := len(embedder.batchSizes)
	if err := indexer.IndexVersion(ctx, versionOne.ID); err != nil {
		t.Fatalf("repeat first version indexing: %v", err)
	}
	if len(embedder.batchSizes) != embeddingCalls {
		t.Fatal("active version was embedded again after duplicate delivery")
	}

	versionTwo := integrationVersion("version-2", document.ID, 2, "第二版答案。", identity)
	versionThree := integrationVersion("version-3", document.ID, 3, "第三版答案。", identity)
	if err := repository.CreateVersionAndIndexJob(ctx, versionTwo, "job-2"); err != nil {
		t.Fatalf("create second version: %v", err)
	}
	if err := repository.CreateVersionAndIndexJob(ctx, versionThree, "job-3"); err != nil {
		t.Fatalf("create third version: %v", err)
	}
	if err := indexer.IndexVersion(ctx, versionThree.ID); err != nil {
		t.Fatalf("index third version: %v", err)
	}
	if err := indexer.IndexVersion(ctx, versionTwo.ID); err != nil {
		t.Fatalf("index superseded second version: %v", err)
	}
	source, err = repository.LoadIndexSource(ctx, versionThree.ID)
	if err != nil {
		t.Fatalf("load third source: %v", err)
	}
	if !source.Active {
		t.Fatal("out-of-order older job replaced the newest active version")
	}
	source, err = repository.LoadIndexSource(ctx, versionTwo.ID)
	if err != nil {
		t.Fatalf("load second source: %v", err)
	}
	if source.Active || source.Status != domain.IndexStatusReady {
		t.Fatalf("unexpected superseded source: %#v", source)
	}
}

func openIndexerTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	schemaName := fmt.Sprintf("knowledge_indexer_test_%d", time.Now().UnixNano())
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

func integrationVersion(
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
