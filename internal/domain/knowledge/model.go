package knowledge

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	maxIDLength    = 64
	maxNameLength  = 255
	maxTitleLength = 500
)

// ErrNotFound 表示目标知识实体不存在。
var ErrNotFound = errors.New("knowledge entity not found")

// ErrConflict 表示知识实体违反唯一性或幂等约束。
var ErrConflict = errors.New("knowledge entity conflict")

// ErrInvalidState 表示知识版本状态不允许当前操作。
var ErrInvalidState = errors.New("invalid knowledge state")

// ErrEmbeddingIdentityMismatch 表示索引与当前 Embedder 不属于同一向量空间。
var ErrEmbeddingIdentityMismatch = errors.New("embedding identity mismatch")

// BaseStatus 表示知识库是否参与在线检索。
type BaseStatus string

const (
	// BaseStatusActive 表示知识库可被检索。
	BaseStatusActive BaseStatus = "active"
	// BaseStatusDisabled 表示知识库暂停参与检索。
	BaseStatusDisabled BaseStatus = "disabled"
)

// DocumentType 表示知识来源类型。
type DocumentType string

const (
	// DocumentTypeFAQ 表示结构化 FAQ。
	DocumentTypeFAQ DocumentType = "faq"
	// DocumentTypeMarkdown 表示 Markdown 文档。
	DocumentTypeMarkdown DocumentType = "markdown"
)

// IndexStatus 表示不可变文档版本的索引生命周期。
type IndexStatus string

const (
	// IndexStatusPending 表示版本等待索引。
	IndexStatusPending IndexStatus = "pending"
	// IndexStatusIndexing 表示版本正在生成切片和向量。
	IndexStatusIndexing IndexStatus = "indexing"
	// IndexStatusReady 表示版本已完成索引，可以发布。
	IndexStatusReady IndexStatus = "ready"
	// IndexStatusFailed 表示版本索引失败，等待重试或人工处理。
	IndexStatusFailed IndexStatus = "failed"
)

// IndexJobType 是持久化知识索引任务的稳定类型。
const IndexJobType = "knowledge.index"

// EmbeddingIdentity 唯一标识一个向量空间。
type EmbeddingIdentity struct {
	// Provider 是 embedding 服务供应商。
	Provider string
	// Model 是 embedding 模型名称。
	Model string
	// Dimensions 是向量维度。
	Dimensions int
}

// Validate 校验 embedding 身份可被当前 pgvector Schema 支持。
func (identity EmbeddingIdentity) Validate() error {
	if strings.TrimSpace(identity.Provider) == "" {
		return fmt.Errorf("embedding provider must not be blank")
	}
	if strings.TrimSpace(identity.Model) == "" {
		return fmt.Errorf("embedding model must not be blank")
	}
	if identity.Dimensions != 1024 {
		return fmt.Errorf("embedding dimensions must be 1024")
	}
	return nil
}

// Equal 判断两个身份是否属于完全相同的向量空间。
func (identity EmbeddingIdentity) Equal(other EmbeddingIdentity) bool {
	return identity.Provider == other.Provider &&
		identity.Model == other.Model &&
		identity.Dimensions == other.Dimensions
}

// Base 表示知识库。
type Base struct {
	// ID 是调用方生成的稳定标识。
	ID string
	// Name 是管理员可见名称。
	Name string
	// Description 是知识库用途说明。
	Description string
	// Status 决定知识库是否参与检索。
	Status BaseStatus
}

// Validate 校验知识库字段。
func (base Base) Validate() error {
	if err := validateID("knowledge base ID", base.ID); err != nil {
		return err
	}
	if strings.TrimSpace(base.Name) == "" || len(base.Name) > maxNameLength {
		return fmt.Errorf("knowledge base name must be 1-%d characters", maxNameLength)
	}
	switch base.Status {
	case BaseStatusActive, BaseStatusDisabled:
	default:
		return fmt.Errorf("invalid knowledge base status %q", base.Status)
	}
	return nil
}

// Document 表示 FAQ 或 Markdown 的逻辑文档。
type Document struct {
	// ID 是调用方生成的稳定标识。
	ID string
	// KnowledgeBaseID 是所属知识库。
	KnowledgeBaseID string
	// Type 是 FAQ 或 Markdown。
	Type DocumentType
	// Title 是来源标题；FAQ 可使用问题作为标题。
	Title string
	// Metadata 是可用于过滤的业务元数据。
	Metadata map[string]any
}

// Validate 校验逻辑文档字段。
func (document Document) Validate() error {
	if err := validateID("document ID", document.ID); err != nil {
		return err
	}
	if err := validateID("knowledge base ID", document.KnowledgeBaseID); err != nil {
		return err
	}
	switch document.Type {
	case DocumentTypeFAQ, DocumentTypeMarkdown:
	default:
		return fmt.Errorf("invalid document type %q", document.Type)
	}
	if strings.TrimSpace(document.Title) == "" || len(document.Title) > maxTitleLength {
		return fmt.Errorf("document title must be 1-%d characters", maxTitleLength)
	}
	return nil
}

// Version 表示文档的一次不可变内容快照。
type Version struct {
	// ID 是版本稳定标识。
	ID string
	// DocumentID 是所属逻辑文档。
	DocumentID string
	// Number 是同一文档内单调递增的版本号。
	Number int
	// Content 是待切片的规范化源内容。
	Content string
	// ContentSHA256 用于检测内容意外变化。
	ContentSHA256 string
	// EmbeddingIdentity 是该版本索引使用的向量空间。
	EmbeddingIdentity EmbeddingIdentity
}

// Validate 校验不可变版本及内容校验和。
func (version Version) Validate() error {
	if err := validateID("version ID", version.ID); err != nil {
		return err
	}
	if err := validateID("document ID", version.DocumentID); err != nil {
		return err
	}
	if version.Number <= 0 {
		return fmt.Errorf("version number must be greater than zero")
	}
	if strings.TrimSpace(version.Content) == "" {
		return fmt.Errorf("version content must not be blank")
	}
	if version.ContentSHA256 != ContentChecksum(version.Content) {
		return fmt.Errorf("version content checksum does not match content")
	}
	return version.EmbeddingIdentity.Validate()
}

// ContentChecksum 返回内容的十六进制 SHA-256。
func ContentChecksum(content string) string {
	checksum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", checksum)
}

// Chunk 表示一个可检索知识切片。
type Chunk struct {
	// ID 是切片稳定标识。
	ID string
	// VersionID 是所属不可变版本。
	VersionID string
	// Position 是切片在版本内的零基顺序。
	Position int
	// Content 是进入检索上下文的原始文本。
	Content string
	// TokenCount 是切片估算 Token 数。
	TokenCount int
	// Metadata 是用于检索过滤的切片元数据。
	Metadata map[string]any
	// Embedding 是与版本身份一致的向量。
	Embedding []float64
}

// Validate 校验切片内容、顺序和向量。
func (chunk Chunk) Validate(identity EmbeddingIdentity) error {
	if err := validateID("chunk ID", chunk.ID); err != nil {
		return err
	}
	if err := validateID("version ID", chunk.VersionID); err != nil {
		return err
	}
	if chunk.Position < 0 {
		return fmt.Errorf("chunk position must not be negative")
	}
	if strings.TrimSpace(chunk.Content) == "" {
		return fmt.Errorf("chunk content must not be blank")
	}
	if chunk.TokenCount < 0 {
		return fmt.Errorf("chunk token count must not be negative")
	}
	if err := validateVector("chunk embedding", chunk.Embedding, identity.Dimensions); err != nil {
		return err
	}
	return nil
}

// SearchQuery 定义活动知识版本检索参数。
type SearchQuery struct {
	// KnowledgeBaseID 限定单个知识库。
	KnowledgeBaseID string
	// EmbeddingIdentity 必须与所有活动版本完全一致。
	EmbeddingIdentity EmbeddingIdentity
	// Embedding 是用户问题向量。
	Embedding []float64
	// Metadata 是 JSON 包含匹配过滤条件。
	Metadata map[string]any
	// Limit 是最大返回数量。
	Limit int
	// MinimumSimilarity 是最小 cosine similarity。
	MinimumSimilarity float64
}

// Validate 校验检索参数。
func (query SearchQuery) Validate() error {
	if err := validateID("knowledge base ID", query.KnowledgeBaseID); err != nil {
		return err
	}
	if err := query.EmbeddingIdentity.Validate(); err != nil {
		return err
	}
	if err := validateVector(
		"query embedding",
		query.Embedding,
		query.EmbeddingIdentity.Dimensions,
	); err != nil {
		return err
	}
	if query.Limit <= 0 || query.Limit > 100 {
		return fmt.Errorf("search limit must be between 1 and 100")
	}
	if query.MinimumSimilarity < -1 || query.MinimumSimilarity > 1 {
		return fmt.Errorf("minimum similarity must be between -1 and 1")
	}
	return nil
}

// SearchResult 表示一个活动版本切片命中。
type SearchResult struct {
	// ChunkID 是命中切片。
	ChunkID string
	// DocumentID 是引用来源文档。
	DocumentID string
	// VersionID 是命中版本。
	VersionID string
	// DocumentType 是 FAQ 或 Markdown。
	DocumentType DocumentType
	// Title 是引用展示标题。
	Title string
	// Content 是切片原文。
	Content string
	// Metadata 是切片元数据。
	Metadata map[string]any
	// Similarity 是 cosine similarity。
	Similarity float64
	// Rank 是从一开始的返回顺序。
	Rank int
}

func validateID(name string, value string) error {
	if strings.TrimSpace(value) == "" || len(value) > maxIDLength {
		return fmt.Errorf("%s must be 1-%d characters", name, maxIDLength)
	}
	return nil
}

func validateVector(name string, vector []float64, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf(
			"%s dimensions mismatch: expected %d, got %d",
			name,
			dimensions,
			len(vector),
		)
	}
	hasNonZeroValue := false
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must contain only finite values", name)
		}
		if value != 0 {
			hasNonZeroValue = true
		}
	}
	if !hasNonZeroValue {
		return fmt.Errorf("%s must not be a zero vector", name)
	}
	return nil
}
