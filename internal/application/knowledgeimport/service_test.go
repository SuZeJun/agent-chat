package knowledgeimport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	created domain.FAQImport
	result  domain.CreateFAQImportResult
	err     error
}

func (repository *fakeRepository) CreateFAQImport(
	_ context.Context,
	knowledgeImport domain.FAQImport,
) (domain.CreateFAQImportResult, error) {
	repository.created = knowledgeImport
	if repository.err != nil {
		return domain.CreateFAQImportResult{}, repository.err
	}
	if repository.result.Snapshot.ID == "" {
		repository.result.Snapshot = domain.FAQImportSnapshot{
			ID:              knowledgeImport.ID,
			KnowledgeBaseID: knowledgeImport.KnowledgeBaseID,
			SourceName:      knowledgeImport.SourceName,
			ContentSHA256:   knowledgeImport.ContentSHA256,
			Status:          domain.IndexStatusPending,
			TotalRows:       len(knowledgeImport.Items),
			CreatedAt:       knowledgeImport.CreatedAt,
		}
	}
	return repository.result, nil
}

func (repository *fakeRepository) LoadFAQImport(
	_ context.Context,
	_ string,
	_ string,
) (domain.FAQImportSnapshot, error) {
	if repository.err != nil {
		return domain.FAQImportSnapshot{}, repository.err
	}
	return repository.result.Snapshot, nil
}

type sequentialGenerator struct {
	next int
}

func (generator *sequentialGenerator) NewID(prefix string) string {
	generator.next++
	return prefix + string(rune('a'-1+generator.next))
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

func TestImportFAQsBuildsAtomicSubmission(t *testing.T) {
	repository := &fakeRepository{}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	service := newTestService(t, repository, now)

	result, err := service.ImportFAQs(context.Background(), ImportRequest{
		KnowledgeBaseID: "base-1",
		SourceName:      `C:\fakepath\faq.csv`,
		Content: []byte(
			"\xef\xbb\xbfquestion,answer,source_url\r\n" +
				"如何重置密码？,请在设置页重置。,https://docs.example.com/reset\r\n" +
				"支持哪些区域？,\"华北、华东\",",
		),
	})
	if err != nil {
		t.Fatalf("ImportFAQs returned error: %v", err)
	}
	if result.ImportID == "" || result.Status != domain.IndexStatusPending ||
		result.TotalRows != 2 || result.Duplicate {
		t.Fatalf("unexpected import result: %#v", result)
	}
	if err := repository.created.Validate(); err != nil {
		t.Fatalf("invalid import submission: %v", err)
	}
	if repository.created.SourceName != "faq.csv" ||
		repository.created.CreatedAt != now ||
		len(repository.created.Items) != 2 {
		t.Fatalf("unexpected import submission: %#v", repository.created)
	}
	first := repository.created.Items[0]
	if first.Document.Title != "如何重置密码？" ||
		first.Version.Content != "请在设置页重置。" ||
		first.Document.Metadata["source_url"] != "https://docs.example.com/reset" ||
		first.Version.EmbeddingIdentity.Model != "embedding-3" {
		t.Fatalf("unexpected first FAQ item: %#v", first)
	}
}

func TestImportFAQsCanonicalChecksumIgnoresBOMAndNewlines(t *testing.T) {
	firstRepository := &fakeRepository{}
	secondRepository := &fakeRepository{}
	now := time.Now()
	firstService := newTestService(t, firstRepository, now)
	secondService := newTestService(t, secondRepository, now)
	first := []byte("\xef\xbb\xbfquestion,answer\r\n问题,答案\r\n")
	second := []byte("question,answer\n问题,答案\n")

	if _, err := firstService.ImportFAQs(context.Background(), ImportRequest{
		KnowledgeBaseID: "base-1",
		SourceName:      "first.csv",
		Content:         first,
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := secondService.ImportFAQs(context.Background(), ImportRequest{
		KnowledgeBaseID: "base-1",
		SourceName:      "second.csv",
		Content:         second,
	}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if firstRepository.created.ContentSHA256 != secondRepository.created.ContentSHA256 {
		t.Fatal("canonical equivalent CSV files produced different checksums")
	}
}

func TestImportFAQsRejectsInvalidCSV(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "header", content: "q,a\n问题,答案\n"},
		{name: "empty", content: "question,answer\n"},
		{name: "blank question", content: "question,answer\n,答案\n"},
		{name: "duplicate", content: "question,answer\n问题,答案\n问题,另一个答案\n"},
		{name: "invalid URL", content: "question,answer,source_url\n问题,答案,file:///etc/passwd\n"},
		{name: "extra field", content: "question,answer\n问题,答案,额外\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(t, &fakeRepository{}, time.Now())
			_, err := service.ImportFAQs(context.Background(), ImportRequest{
				KnowledgeBaseID: "base-1",
				SourceName:      "faq.csv",
				Content:         []byte(test.content),
			})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "invalid_faq_csv" {
				t.Fatalf("expected invalid_faq_csv, got %v", err)
			}
		})
	}
}

func TestImportFAQsLimitsFileSize(t *testing.T) {
	service := newTestService(t, &fakeRepository{}, time.Now())
	_, err := service.ImportFAQs(context.Background(), ImportRequest{
		KnowledgeBaseID: "base-1",
		SourceName:      "faq.csv",
		Content:         []byte(strings.Repeat("x", maxCSVBytes+1)),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "invalid_faq_csv" {
		t.Fatalf("expected invalid_faq_csv, got %v", err)
	}
}

func TestImportFAQsMapsRepositoryErrors(t *testing.T) {
	tests := []struct {
		name      string
		cause     error
		code      string
		retryable bool
	}{
		{name: "not found", cause: domain.ErrNotFound, code: "knowledge_base_not_found"},
		{name: "database", cause: errors.New("database unavailable"), code: "faq_import_failed", retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(t, &fakeRepository{err: test.cause}, time.Now())
			_, err := service.ImportFAQs(context.Background(), ImportRequest{
				KnowledgeBaseID: "base-1",
				SourceName:      "faq.csv",
				Content:         []byte("question,answer\n问题,答案\n"),
			})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != test.code ||
				failure.RetryAllowed != test.retryable {
				t.Fatalf("unexpected failure: %#v", failure)
			}
		})
	}
}

func newTestService(
	t *testing.T,
	repository Repository,
	now time.Time,
) *Service {
	t.Helper()
	service, err := NewService(
		repository,
		domain.EmbeddingIdentity{
			Provider:   "zhipu",
			Model:      "embedding-3",
			Dimensions: 1024,
		},
		&sequentialGenerator{},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	return service
}
