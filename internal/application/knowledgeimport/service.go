package knowledgeimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	domain "agent-chat/internal/domain/knowledge"

	"github.com/google/uuid"
)

const (
	maxCSVBytes       = 2 << 20
	maxFAQRows        = 1000
	maxQuestionRunes  = 500
	maxAnswerRunes    = 16_000
	maxSourceURLBytes = 2048
)

// Repository 定义 FAQ 导入用例所需的最小持久化能力。
type Repository interface {
	CreateFAQImport(context.Context, domain.FAQImport) (domain.CreateFAQImportResult, error)
	LoadFAQImport(context.Context, string, string) (domain.FAQImportSnapshot, error)
}

// IDGenerator 为导入、文档、版本和 Job 生成稳定长度的 ID。
type IDGenerator interface {
	NewID(prefix string) string
}

// Clock 为导入用例提供可测试时间。
type Clock interface {
	Now() time.Time
}

// UUIDGenerator 使用不含连字符的 UUID 生成业务 ID。
type UUIDGenerator struct{}

// NewID 生成满足数据库长度约束的随机 ID。
func (UUIDGenerator) NewID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// SystemClock 使用系统 UTC 时间。
type SystemClock struct{}

// Now 返回当前 UTC 时间。
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// ImportRequest 是管理员上传 FAQ CSV 的 Application 输入。
type ImportRequest struct {
	KnowledgeBaseID string
	SourceName      string
	Content         []byte
}

// ImportResult 返回导入状态轮询所需信息。
type ImportResult struct {
	ImportID  string
	Status    domain.IndexStatus
	TotalRows int
	Duplicate bool
}

// Failure 是可安全映射到管理员 API 的稳定错误。
type Failure struct {
	Code         string
	RetryAllowed bool
	// UserMessage 只包含由确定性校验器生成、可向管理员展示的原因。
	UserMessage string
	cause       error
}

// Error 返回不包含 CSV 内容、数据库细节或内部路径的稳定错误码。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅用于进程内错误分类。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// CanRetry 表示调用方能否安全重试。
func (failure *Failure) CanRetry() bool {
	return failure.RetryAllowed
}

// Service 编排 FAQ CSV 解析、规范化和原子导入。
type Service struct {
	repository  Repository
	identity    domain.EmbeddingIdentity
	idGenerator IDGenerator
	clock       Clock
}

// NewService 创建 FAQ 导入服务。
func NewService(
	repository Repository,
	identity domain.EmbeddingIdentity,
	idGenerator IDGenerator,
	clock Clock,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("FAQ import repository is required")
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("FAQ import embedding identity: %w", err)
	}
	if idGenerator == nil {
		return nil, errors.New("FAQ import ID generator is required")
	}
	if clock == nil {
		return nil, errors.New("FAQ import clock is required")
	}
	return &Service{
		repository:  repository,
		identity:    identity,
		idGenerator: idGenerator,
		clock:       clock,
	}, nil
}

// ImportFAQs 解析 CSV，并一次性创建全部 FAQ 文档、版本和索引任务。
func (service *Service) ImportFAQs(
	ctx context.Context,
	request ImportRequest,
) (ImportResult, error) {
	request.KnowledgeBaseID = strings.TrimSpace(request.KnowledgeBaseID)
	request.SourceName = strings.TrimSpace(path.Base(
		strings.ReplaceAll(request.SourceName, `\`, "/"),
	))
	if request.KnowledgeBaseID == "" || len(request.KnowledgeBaseID) > 64 {
		return ImportResult{}, newFailure("invalid_knowledge_base_id", false, nil)
	}
	if !validImportSourceName(request.SourceName) {
		return ImportResult{}, newFailure("invalid_import_source_name", false, nil)
	}

	rows, checksum, err := parseFAQCSV(request.Content)
	if err != nil {
		return ImportResult{}, &Failure{
			Code:         "invalid_faq_csv",
			RetryAllowed: false,
			UserMessage:  err.Error(),
			cause:        err,
		}
	}
	createdAt := service.clock.Now().UTC()
	knowledgeImport := domain.FAQImport{
		ID:              service.idGenerator.NewID("imp_"),
		KnowledgeBaseID: request.KnowledgeBaseID,
		SourceName:      request.SourceName,
		ContentSHA256:   checksum,
		Items:           make([]domain.FAQImportItem, len(rows)),
		CreatedAt:       createdAt,
	}
	for index, row := range rows {
		documentID := service.idGenerator.NewID("doc_")
		versionID := service.idGenerator.NewID("ver_")
		metadata := map[string]any{
			"import_id":   knowledgeImport.ID,
			"csv_row":     index + 2,
			"source_name": request.SourceName,
		}
		if row.SourceURL != "" {
			metadata["source_url"] = row.SourceURL
		}
		knowledgeImport.Items[index] = domain.FAQImportItem{
			RowNumber: index + 2,
			Document: domain.Document{
				ID:              documentID,
				KnowledgeBaseID: request.KnowledgeBaseID,
				Type:            domain.DocumentTypeFAQ,
				Title:           row.Question,
				Metadata:        metadata,
			},
			Version: domain.Version{
				ID:                versionID,
				DocumentID:        documentID,
				Number:            1,
				Content:           row.Answer,
				ContentSHA256:     domain.ContentChecksum(row.Answer),
				EmbeddingIdentity: service.identity,
			},
			JobID: service.idGenerator.NewID("job_"),
		}
	}

	result, err := service.repository.CreateFAQImport(ctx, knowledgeImport)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return ImportResult{}, err
		case errors.Is(err, domain.ErrNotFound):
			return ImportResult{}, newFailure("knowledge_base_not_found", false, err)
		default:
			return ImportResult{}, newFailure("faq_import_failed", true, err)
		}
	}
	return ImportResult{
		ImportID:  result.Snapshot.ID,
		Status:    result.Snapshot.Status,
		TotalRows: result.Snapshot.TotalRows,
		Duplicate: result.Duplicate,
	}, nil
}

// GetStatus 返回属于指定知识库的导入状态。
func (service *Service) GetStatus(
	ctx context.Context,
	knowledgeBaseID string,
	importID string,
) (domain.FAQImportSnapshot, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	importID = strings.TrimSpace(importID)
	if knowledgeBaseID == "" || len(knowledgeBaseID) > 64 ||
		importID == "" || len(importID) > 64 {
		return domain.FAQImportSnapshot{}, newFailure("invalid_faq_import_scope", false, nil)
	}
	snapshot, err := service.repository.LoadFAQImport(ctx, knowledgeBaseID, importID)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return domain.FAQImportSnapshot{}, err
		case errors.Is(err, domain.ErrNotFound):
			return domain.FAQImportSnapshot{}, newFailure("faq_import_not_found", false, err)
		default:
			return domain.FAQImportSnapshot{}, newFailure("load_faq_import_failed", true, err)
		}
	}
	return snapshot, nil
}

// validImportSourceName 校验归一化后的来源文件名。
//
// path.Base 在输入为空时返回 "."，输入为 "/" 时原样返回 "/"，这些都是路径占位
// 结果而非文件名；若不显式排除，空文件名会以 "." 的形式持久化到导入记录和每条
// 文档的 metadata 上。
func validImportSourceName(name string) bool {
	switch name {
	case "", ".", "..", "/":
		return false
	}
	return len(name) <= 255
}

// faqRow 是经过表头映射但尚未完成业务校验的一行 CSV。
type faqRow struct {
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	SourceURL string `json:"sourceUrl,omitempty"`
}

// parseFAQCSV 校验 UTF-8、表头、行数和重复问题，并基于规范化内容计算幂等校验和。
func parseFAQCSV(content []byte) ([]faqRow, string, error) {
	if len(content) == 0 || len(content) > maxCSVBytes {
		return nil, "", errors.New("CSV size is invalid")
	}
	if !utf8.Valid(content) {
		return nil, "", errors.New("CSV must be UTF-8")
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, "", errors.New("CSV header is required")
	}
	hasSourceURL, err := validateHeader(header)
	if err != nil {
		return nil, "", err
	}

	rows := make([]faqRow, 0)
	questions := make(map[string]struct{})
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", fmt.Errorf("CSV row %d is invalid", rowNumber)
		}
		expectedFields := 2
		if hasSourceURL {
			expectedFields = 3
		}
		if len(record) != expectedFields {
			return nil, "", fmt.Errorf("CSV row %d field count is invalid", rowNumber)
		}
		row := faqRow{
			Question: normalizeField(record[0]),
			Answer:   normalizeField(record[1]),
		}
		if hasSourceURL {
			row.SourceURL = strings.TrimSpace(record[2])
		}
		if err := row.validate(); err != nil {
			return nil, "", fmt.Errorf("CSV row %d: %w", rowNumber, err)
		}
		questionKey := strings.ToLower(row.Question)
		if _, exists := questions[questionKey]; exists {
			return nil, "", fmt.Errorf("CSV row %d duplicates a question", rowNumber)
		}
		questions[questionKey] = struct{}{}
		rows = append(rows, row)
		if len(rows) > maxFAQRows {
			return nil, "", fmt.Errorf("CSV exceeds %d FAQ rows", maxFAQRows)
		}
	}
	if len(rows) == 0 {
		return nil, "", errors.New("CSV contains no FAQ rows")
	}
	canonical, err := json.Marshal(rows)
	if err != nil {
		return nil, "", errors.New("CSV canonicalization failed")
	}
	checksum := sha256.Sum256(canonical)
	return rows, hex.EncodeToString(checksum[:]), nil
}

// validateHeader 只允许两个固定表头，返回是否包含可选 source_url 列。
func validateHeader(header []string) (bool, error) {
	if len(header) != 2 && len(header) != 3 {
		return false, errors.New("CSV header must be question,answer[,source_url]")
	}
	for index := range header {
		header[index] = strings.ToLower(strings.TrimSpace(header[index]))
	}
	if header[0] != "question" || header[1] != "answer" {
		return false, errors.New("CSV header must start with question,answer")
	}
	if len(header) == 3 && header[2] != "source_url" {
		return false, errors.New("CSV third column must be source_url")
	}
	return len(header) == 3, nil
}

// validate 校验 FAQ 字段长度，并将来源 URL 限制为绝对 HTTP(S) 地址。
func (row faqRow) validate() error {
	if row.Question == "" || utf8.RuneCountInString(row.Question) > maxQuestionRunes {
		return fmt.Errorf("question must be 1-%d characters", maxQuestionRunes)
	}
	if row.Answer == "" || utf8.RuneCountInString(row.Answer) > maxAnswerRunes {
		return fmt.Errorf("answer must be 1-%d characters", maxAnswerRunes)
	}
	if row.SourceURL == "" {
		return nil
	}
	if len(row.SourceURL) > maxSourceURLBytes {
		return errors.New("source_url is too long")
	}
	parsed, err := url.ParseRequestURI(row.SourceURL)
	if err != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" {
		return errors.New("source_url must be an absolute HTTP(S) URL")
	}
	return nil
}

func normalizeField(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	return strings.TrimSpace(value)
}

func newFailure(code string, retryAllowed bool, cause error) error {
	return &Failure{Code: code, RetryAllowed: retryAllowed, cause: cause}
}
