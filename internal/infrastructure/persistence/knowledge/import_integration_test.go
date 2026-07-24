package knowledgepg

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	domain "agent-chat/internal/domain/knowledge"

	"github.com/jackc/pgx/v5"
)

func TestFAQImportLifecycleAndIdempotencyAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openKnowledgeTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := NewRepository(pool)
	if err := repository.CreateBase(ctx, domain.Base{
		ID:     "base-import",
		Name:   "FAQ 导入测试",
		Status: domain.BaseStatusActive,
	}); err != nil {
		t.Fatalf("create base: %v", err)
	}
	identity := domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	knowledgeImport := testFAQImport("import-1", "base-import", "same-checksum", identity)

	created, err := repository.CreateFAQImport(ctx, knowledgeImport)
	if err != nil {
		t.Fatalf("create FAQ import: %v", err)
	}
	if created.Duplicate ||
		created.Snapshot.ID != knowledgeImport.ID ||
		created.Snapshot.Status != domain.IndexStatusPending ||
		created.Snapshot.TotalRows != 2 {
		t.Fatalf("unexpected created import: %#v", created)
	}
	assertImportEntityCounts(t, ctx, pool, 1, 2, 2, 2)

	replayed := testFAQImport("import-duplicate", "base-import", "same-checksum", identity)
	replayedResult, err := repository.CreateFAQImport(ctx, replayed)
	if err != nil {
		t.Fatalf("replay FAQ import: %v", err)
	}
	if !replayedResult.Duplicate || replayedResult.Snapshot.ID != knowledgeImport.ID {
		t.Fatalf("unexpected replay result: %#v", replayedResult)
	}
	assertImportEntityCounts(t, ctx, pool, 1, 2, 2, 2)

	if _, err := pool.Exec(ctx, `
		UPDATE knowledge_document_versions
		SET index_status = 'failed',
		    index_error = 'embedding_unavailable'
		WHERE id = 'version-import-1-1'
	`); err != nil {
		t.Fatalf("mark retrying version failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'retry_wait',
		    attempts = 1,
		    available_at = now() + interval '1 minute',
		    last_error = 'embedding_unavailable',
		    updated_at = now()
		WHERE id = 'job-import-1-1'
	`); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	retrying, err := repository.LoadFAQImport(ctx, "base-import", knowledgeImport.ID)
	if err != nil {
		t.Fatalf("load retrying import: %v", err)
	}
	if retrying.Status != domain.IndexStatusIndexing ||
		retrying.Items[0].Status != domain.IndexStatusIndexing ||
		retrying.Items[0].ErrorCode != "" {
		t.Fatalf("unexpected retrying import: %#v", retrying)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'failed',
		    attempts = max_attempts,
		    available_at = now(),
		    last_error = 'embedding_unavailable',
		    updated_at = now()
		WHERE id = 'job-import-1-1'
	`); err != nil {
		t.Fatalf("fail import job: %v", err)
	}
	failed, err := repository.LoadFAQImport(ctx, "base-import", knowledgeImport.ID)
	if err != nil {
		t.Fatalf("load failed import: %v", err)
	}
	if failed.Status != domain.IndexStatusFailed ||
		failed.FailedRows != 1 ||
		failed.Items[0].ErrorCode != "embedding_unavailable" {
		t.Fatalf("unexpected failed import: %#v", failed)
	}

	_, err = repository.LoadFAQImport(ctx, "another-base", knowledgeImport.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected scoped import lookup to fail, got %v", err)
	}
}

func TestFAQImportConcurrentReplayAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openKnowledgeTestDatabase(t, ctx, databaseURL)
	defer pool.Close()

	repository := NewRepository(pool)
	if err := repository.CreateBase(ctx, domain.Base{
		ID:     "base-import-concurrent",
		Name:   "并发导入测试",
		Status: domain.BaseStatusActive,
	}); err != nil {
		t.Fatalf("create base: %v", err)
	}
	identity := domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}

	const callers = 8
	results := make(chan domain.CreateFAQImportResult, callers)
	failures := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for index := 0; index < callers; index++ {
		waitGroup.Add(1)
		go func(call int) {
			defer waitGroup.Done()
			result, err := repository.CreateFAQImport(
				ctx,
				testFAQImport(
					"import-concurrent-"+string(rune('a'+call)),
					"base-import-concurrent",
					"concurrent-checksum",
					identity,
				),
			)
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}(index)
	}
	waitGroup.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent import failed: %v", err)
	}
	firstID := ""
	createdCount := 0
	for result := range results {
		if firstID == "" {
			firstID = result.Snapshot.ID
		}
		if result.Snapshot.ID != firstID {
			t.Fatalf("concurrent imports returned different IDs: %s and %s", firstID, result.Snapshot.ID)
		}
		if !result.Duplicate {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected one creator, got %d", createdCount)
	}
	assertImportEntityCounts(t, ctx, pool, 1, 2, 2, 2)
}

func testFAQImport(
	importID string,
	baseID string,
	checksumSeed string,
	identity domain.EmbeddingIdentity,
) domain.FAQImport {
	createdAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	items := make([]domain.FAQImportItem, 2)
	for index := range items {
		suffix := string(rune('1' + index))
		answer := "答案-" + suffix
		items[index] = domain.FAQImportItem{
			RowNumber: index + 2,
			Document: domain.Document{
				ID:              "document-" + importID + "-" + suffix,
				KnowledgeBaseID: baseID,
				Type:            domain.DocumentTypeFAQ,
				Title:           "问题-" + suffix,
				Metadata:        map[string]any{"csv_row": index + 2},
			},
			Version: domain.Version{
				ID:                "version-" + importID + "-" + suffix,
				DocumentID:        "document-" + importID + "-" + suffix,
				Number:            1,
				Content:           answer,
				ContentSHA256:     domain.ContentChecksum(answer),
				EmbeddingIdentity: identity,
			},
			JobID: "job-" + importID + "-" + suffix,
		}
	}
	return domain.FAQImport{
		ID:              importID,
		KnowledgeBaseID: baseID,
		SourceName:      "faq.csv",
		ContentSHA256:   domain.ContentChecksum(checksumSeed),
		Items:           items,
		CreatedAt:       createdAt,
	}
}

func assertImportEntityCounts(
	t *testing.T,
	ctx context.Context,
	pool queryRower,
	imports int,
	documents int,
	versions int,
	jobs int,
) {
	t.Helper()
	assertTableCount(t, ctx, pool, "knowledge_imports", imports)
	assertTableCount(t, ctx, pool, "knowledge_documents", documents)
	assertTableCount(t, ctx, pool, "knowledge_document_versions", versions)
	assertTableCount(t, ctx, pool, "jobs", jobs)
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func assertTableCount(
	t *testing.T,
	ctx context.Context,
	pool queryRower,
	table string,
	expected int,
) {
	t.Helper()
	var count int
	query := "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize()
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != expected {
		t.Fatalf("unexpected %s count: got %d want %d", table, count, expected)
	}
}
