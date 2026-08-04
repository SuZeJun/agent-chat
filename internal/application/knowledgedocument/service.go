package knowledgedocument

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	domain "agent-chat/internal/domain/knowledge"
)

// MaxContentBytes 限制单个 Markdown 版本的 UTF-8 内容大小。
const MaxContentBytes = 512 << 10

// VersionItem 是管理员可见的不可变版本索引状态。
type VersionItem struct {
	ID        string
	Number    int
	Status    domain.IndexStatus
	ErrorCode string
	Active    bool
	CreatedAt time.Time
	IndexedAt *time.Time
}

// DocumentItem 是 Markdown 文档列表中的安全摘要。
type DocumentItem struct {
	ID              string
	KnowledgeBaseID string
	Title           string
	SourceURL       string
	ActiveVersionID string
	LatestVersion   int
	Versions        []VersionItem
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// DocumentDetail 在摘要之外返回最新不可变版本的源内容，供管理员创建新版本。
type DocumentDetail struct {
	DocumentItem
	LatestContent string
}

// CreateCommand 是 Repository 原子创建逻辑文档、首版本和 Job 的输入。
type CreateCommand struct {
	Document domain.Document
	Version  domain.Version
	JobID    string
}

// CreateVersionCommand 由 Repository 在文档行锁内分配单调版本号。
type CreateVersionCommand struct {
	KnowledgeBaseID   string
	DocumentID        string
	VersionID         string
	Content           string
	ContentSHA256     string
	EmbeddingIdentity domain.EmbeddingIdentity
	JobID             string
}

// Repository 定义 Markdown 文档管理所需的事务性持久化能力。
type Repository interface {
	CreateMarkdownDocument(context.Context, CreateCommand) (DocumentDetail, error)
	CreateMarkdownVersion(context.Context, CreateVersionCommand) (DocumentDetail, error)
	ListMarkdownDocuments(context.Context, string) ([]DocumentItem, error)
	LoadMarkdownDocument(context.Context, string, string) (DocumentDetail, error)
	RetryMarkdownVersion(context.Context, string, string, string) (DocumentDetail, error)
}

// IDGenerator 生成文档、版本和 Job 的稳定 ID。
type IDGenerator interface {
	NewID(prefix string) string
}

// CreateRequest 是管理员创建 Markdown 逻辑文档的输入。
type CreateRequest struct {
	KnowledgeBaseID string
	Title           string
	Content         string
	SourceURL       string
}

// CreateVersionRequest 是管理员为既有文档创建新版本的输入。
type CreateVersionRequest struct {
	KnowledgeBaseID string
	DocumentID      string
	Content         string
}

// Failure 是可安全映射到管理员 API 的稳定错误。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回稳定错误码。
func (failure *Failure) Error() string { return failure.Code }

// Unwrap 仅用于进程内错误分类。
func (failure *Failure) Unwrap() error { return failure.cause }

// Service 管理 Markdown 文档及其不可变版本。
type Service struct {
	repository  Repository
	identity    domain.EmbeddingIdentity
	idGenerator IDGenerator
}

// NewService 创建 Markdown 文档 Application Service。
func NewService(
	repository Repository,
	identity domain.EmbeddingIdentity,
	idGenerator IDGenerator,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("Markdown document repository is required")
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if idGenerator == nil {
		return nil, errors.New("Markdown document ID generator is required")
	}
	return &Service{repository: repository, identity: identity, idGenerator: idGenerator}, nil
}

// Create 原子创建 Markdown 逻辑文档、首版本和持久化索引 Job。
func (service *Service) Create(ctx context.Context, request CreateRequest) (DocumentDetail, error) {
	request.KnowledgeBaseID = strings.TrimSpace(request.KnowledgeBaseID)
	request.Title = strings.TrimSpace(request.Title)
	request.Content = normalizeContent(request.Content)
	request.SourceURL = strings.TrimSpace(request.SourceURL)
	if err := validateScope(request.KnowledgeBaseID, "invalid_knowledge_base_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateContent(request.Content); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateSourceURL(request.SourceURL); err != nil {
		return DocumentDetail{}, err
	}
	documentID := service.idGenerator.NewID("doc_")
	metadata := map[string]any{}
	if request.SourceURL != "" {
		metadata["source_url"] = request.SourceURL
	}
	document := domain.Document{
		ID: documentID, KnowledgeBaseID: request.KnowledgeBaseID,
		Type: domain.DocumentTypeMarkdown, Title: request.Title, Metadata: metadata,
	}
	version := domain.Version{
		ID: service.idGenerator.NewID("ver_"), DocumentID: documentID, Number: 1,
		Content: request.Content, ContentSHA256: domain.ContentChecksum(request.Content),
		EmbeddingIdentity: service.identity,
	}
	if err := document.Validate(); err != nil {
		return DocumentDetail{}, failure("invalid_markdown_document", false, err)
	}
	if err := version.Validate(); err != nil {
		return DocumentDetail{}, failure("invalid_markdown_document", false, err)
	}
	result, err := service.repository.CreateMarkdownDocument(ctx, CreateCommand{
		Document: document, Version: version, JobID: service.idGenerator.NewID("job_"),
	})
	return result, mapRepositoryError(err, "create_markdown_document_failed")
}

// CreateVersion 为既有 Markdown 文档创建内容不可变的新版本。
func (service *Service) CreateVersion(
	ctx context.Context,
	request CreateVersionRequest,
) (DocumentDetail, error) {
	request.KnowledgeBaseID = strings.TrimSpace(request.KnowledgeBaseID)
	request.DocumentID = strings.TrimSpace(request.DocumentID)
	request.Content = normalizeContent(request.Content)
	if err := validateScope(request.KnowledgeBaseID, "invalid_knowledge_base_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateScope(request.DocumentID, "invalid_document_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateContent(request.Content); err != nil {
		return DocumentDetail{}, err
	}
	result, err := service.repository.CreateMarkdownVersion(ctx, CreateVersionCommand{
		KnowledgeBaseID:   request.KnowledgeBaseID,
		DocumentID:        request.DocumentID,
		VersionID:         service.idGenerator.NewID("ver_"),
		Content:           request.Content,
		ContentSHA256:     domain.ContentChecksum(request.Content),
		EmbeddingIdentity: service.identity,
		JobID:             service.idGenerator.NewID("job_"),
	})
	return result, mapRepositoryError(err, "create_markdown_version_failed")
}

// List 返回指定知识库的 Markdown 文档与版本状态。
func (service *Service) List(ctx context.Context, knowledgeBaseID string) ([]DocumentItem, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	if err := validateScope(knowledgeBaseID, "invalid_knowledge_base_id"); err != nil {
		return nil, err
	}
	items, err := service.repository.ListMarkdownDocuments(ctx, knowledgeBaseID)
	if err != nil {
		return nil, mapRepositoryError(err, "list_markdown_documents_failed")
	}
	if items == nil {
		items = []DocumentItem{}
	}
	return items, nil
}

// Get 返回文档版本状态和最新版本内容。
func (service *Service) Get(
	ctx context.Context,
	knowledgeBaseID string,
	documentID string,
) (DocumentDetail, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	documentID = strings.TrimSpace(documentID)
	if err := validateScope(knowledgeBaseID, "invalid_knowledge_base_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateScope(documentID, "invalid_document_id"); err != nil {
		return DocumentDetail{}, err
	}
	result, err := service.repository.LoadMarkdownDocument(ctx, knowledgeBaseID, documentID)
	return result, mapRepositoryError(err, "load_markdown_document_failed")
}

// Retry 原子重置已耗尽重试的同一索引 Job，不创建重复任务。
func (service *Service) Retry(
	ctx context.Context,
	knowledgeBaseID string,
	documentID string,
	versionID string,
) (DocumentDetail, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	documentID = strings.TrimSpace(documentID)
	versionID = strings.TrimSpace(versionID)
	if err := validateScope(knowledgeBaseID, "invalid_knowledge_base_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateScope(documentID, "invalid_document_id"); err != nil {
		return DocumentDetail{}, err
	}
	if err := validateScope(versionID, "invalid_version_id"); err != nil {
		return DocumentDetail{}, err
	}
	result, err := service.repository.RetryMarkdownVersion(
		ctx, knowledgeBaseID, documentID, versionID,
	)
	return result, mapRepositoryError(err, "retry_markdown_version_failed")
}

func normalizeContent(content string) string {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	return strings.TrimSpace(content)
}

func validateContent(content string) error {
	if content == "" || !utf8.ValidString(content) || len([]byte(content)) > MaxContentBytes {
		return failure("invalid_markdown_content", false, nil)
	}
	return nil
}

func validateScope(value string, code string) error {
	if value == "" || len(value) > 64 {
		return failure(code, false, nil)
	}
	return nil
}

func validateSourceURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 {
		return failure("invalid_source_url", false, nil)
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return failure("invalid_source_url", false, err)
	}
	return nil
}

func mapRepositoryError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return failure("markdown_document_not_found", false, err)
	case errors.Is(err, domain.ErrConflict):
		return failure("markdown_document_conflict", false, err)
	case errors.Is(err, domain.ErrInvalidState):
		return failure("markdown_version_not_retryable", false, err)
	default:
		return failure(fallback, true, err)
	}
}

func failure(code string, retryAllowed bool, cause error) error {
	return &Failure{Code: code, RetryAllowed: retryAllowed, cause: cause}
}
