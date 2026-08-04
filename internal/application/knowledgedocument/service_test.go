package knowledgedocument

import (
	"context"
	"errors"
	"testing"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	createCommand        CreateCommand
	createVersionCommand CreateVersionCommand
	result               DocumentDetail
	items                []DocumentItem
	err                  error
}

func (repository *fakeRepository) CreateMarkdownDocument(_ context.Context, command CreateCommand) (DocumentDetail, error) {
	repository.createCommand = command
	return repository.result, repository.err
}
func (repository *fakeRepository) CreateMarkdownVersion(_ context.Context, command CreateVersionCommand) (DocumentDetail, error) {
	repository.createVersionCommand = command
	return repository.result, repository.err
}
func (repository *fakeRepository) ListMarkdownDocuments(context.Context, string) ([]DocumentItem, error) {
	return repository.items, repository.err
}
func (repository *fakeRepository) LoadMarkdownDocument(context.Context, string, string) (DocumentDetail, error) {
	return repository.result, repository.err
}
func (repository *fakeRepository) RetryMarkdownVersion(context.Context, string, string, string) (DocumentDetail, error) {
	return repository.result, repository.err
}

type sequenceGenerator struct{ next int }

func (generator *sequenceGenerator) NewID(prefix string) string {
	generator.next++
	return prefix + string(rune('0'+generator.next))
}

func testService(t *testing.T, repository Repository) *Service {
	t.Helper()
	service, err := NewService(repository, domain.EmbeddingIdentity{
		Provider: "zhipu", Model: "embedding-3", Dimensions: 1024,
	}, &sequenceGenerator{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func TestCreateMarkdownDocumentBuildsAtomicAggregate(t *testing.T) {
	repository := &fakeRepository{result: DocumentDetail{DocumentItem: DocumentItem{ID: "doc_1"}}}
	service := testService(t, repository)
	result, err := service.Create(context.Background(), CreateRequest{
		KnowledgeBaseID: " base_1 ", Title: " 接入指南 ",
		Content: "# 标题\r\n\r\n正文 ", SourceURL: "https://docs.example.com/guide",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	command := repository.createCommand
	if result.ID != "doc_1" || command.Document.KnowledgeBaseID != "base_1" ||
		command.Document.Title != "接入指南" || command.Version.Content != "# 标题\n\n正文" ||
		command.Version.Number != 1 || command.Document.Metadata["source_url"] == "" ||
		command.JobID == "" {
		t.Fatalf("unexpected command: %#v", command)
	}
}

func TestCreateMarkdownVersionRejectsOversizedAndMapsConflict(t *testing.T) {
	repository := &fakeRepository{err: domain.ErrConflict}
	service := testService(t, repository)
	_, err := service.CreateVersion(context.Background(), CreateVersionRequest{
		KnowledgeBaseID: "base_1", DocumentID: "doc_1", Content: "相同内容",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "markdown_document_conflict" {
		t.Fatalf("unexpected conflict: %v", err)
	}
	_, err = service.CreateVersion(context.Background(), CreateVersionRequest{
		KnowledgeBaseID: "base_1", DocumentID: "doc_1",
		Content: string(make([]byte, MaxContentBytes+1)),
	})
	if !errors.As(err, &failure) || failure.Code != "invalid_markdown_content" {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestListMarkdownDocumentsReturnsNonNilEmptyList(t *testing.T) {
	items, err := testService(t, &fakeRepository{}).List(context.Background(), "base_1")
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("unexpected items=%#v err=%v", items, err)
	}
}

func TestRetryMarkdownVersionMapsInvalidState(t *testing.T) {
	service := testService(t, &fakeRepository{err: domain.ErrInvalidState})
	_, err := service.Retry(context.Background(), "base_1", "doc_1", "ver_1")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "markdown_version_not_retryable" {
		t.Fatalf("unexpected error: %v", err)
	}
}
