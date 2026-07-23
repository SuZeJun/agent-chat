package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"

	"agent-chat/internal/application/knowledgeretrieve"
	domain "agent-chat/internal/domain/knowledge"

	einoretriever "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

const (
	// MetadataFilterKey 是 Eino DSLInfo 中承载 JSON 包含过滤条件的白名单键。
	MetadataFilterKey = "metadata"

	// MetadataKnowledgeBaseID 标识命中所属知识库。
	MetadataKnowledgeBaseID = "knowledge_base_id"
	// MetadataDocumentID 标识引用来源文档。
	MetadataDocumentID = "document_id"
	// MetadataVersionID 标识引用来源版本。
	MetadataVersionID = "version_id"
	// MetadataDocumentType 标识 FAQ 或 Markdown。
	MetadataDocumentType = "document_type"
	// MetadataTitle 是引用展示标题。
	MetadataTitle = "title"
	// MetadataRank 是从一开始的检索排序。
	MetadataRank = "rank"
	// MetadataSimilarity 是 pgvector cosine similarity。
	MetadataSimilarity = "similarity"
)

var _ einoretriever.Retriever = (*KnowledgeRetriever)(nil)

// Service 定义 Eino Retriever 适配器调用的知识检索用例。
type Service interface {
	Retrieve(context.Context, knowledgeretrieve.Request) ([]domain.SearchResult, error)
}

// Config 固定服务端授权后的知识库和默认检索参数。
type Config struct {
	KnowledgeBaseID       string
	DefaultTopK           int
	DefaultScoreThreshold float64
	Metadata              map[string]any
}

// KnowledgeRetriever 将 Eino 标准选项映射到受控的知识检索请求。
type KnowledgeRetriever struct {
	service Service
	config  Config
}

// NewKnowledgeRetriever 创建绑定单个知识库的 Eino Retriever。
//
// 调用级 Index 不得覆盖构造时由服务端确定的知识库，Embedding 也不得覆盖，
// 从而避免模型或 Graph 参数绕过资源授权和向量空间约束。
func NewKnowledgeRetriever(service Service, config Config) (*KnowledgeRetriever, error) {
	if service == nil {
		return nil, errors.New("knowledge retrieval service is required")
	}
	config.KnowledgeBaseID = strings.TrimSpace(config.KnowledgeBaseID)
	if config.KnowledgeBaseID == "" || len(config.KnowledgeBaseID) > 64 {
		return nil, errors.New("knowledge base ID must be 1-64 characters")
	}
	if config.DefaultTopK <= 0 || config.DefaultTopK > 100 {
		return nil, errors.New("default TopK must be between 1 and 100")
	}
	if config.DefaultScoreThreshold < -1 || config.DefaultScoreThreshold > 1 ||
		math.IsNaN(config.DefaultScoreThreshold) ||
		math.IsInf(config.DefaultScoreThreshold, 0) {
		return nil, errors.New("default score threshold must be a finite value between -1 and 1")
	}
	normalizedMetadata, err := normalizeMetadata(config.Metadata)
	if err != nil {
		return nil, errors.New("default metadata filter must be valid JSON")
	}
	config.Metadata = normalizedMetadata
	return &KnowledgeRetriever{service: service, config: config}, nil
}

// Retrieve 实现 Eino Retriever，并返回带来源、分数和排序元数据的 Document。
func (retriever *KnowledgeRetriever) Retrieve(
	ctx context.Context,
	query string,
	options ...einoretriever.Option,
) ([]*schema.Document, error) {
	defaultTopK := retriever.config.DefaultTopK
	defaultThreshold := retriever.config.DefaultScoreThreshold
	commonOptions := einoretriever.GetCommonOptions(&einoretriever.Options{
		TopK:           &defaultTopK,
		ScoreThreshold: &defaultThreshold,
	}, options...)

	if commonOptions.Index != nil &&
		strings.TrimSpace(*commonOptions.Index) != retriever.config.KnowledgeBaseID {
		return nil, errors.New("knowledge_retriever_index_override_rejected")
	}
	if commonOptions.SubIndex != nil {
		return nil, errors.New("knowledge_retriever_sub_index_unsupported")
	}
	if commonOptions.Embedding != nil {
		return nil, errors.New("knowledge_retriever_embedding_override_rejected")
	}
	if commonOptions.TopK == nil ||
		*commonOptions.TopK <= 0 ||
		*commonOptions.TopK > 100 {
		return nil, errors.New("knowledge_retriever_invalid_top_k")
	}
	if commonOptions.ScoreThreshold == nil ||
		*commonOptions.ScoreThreshold < -1 ||
		*commonOptions.ScoreThreshold > 1 ||
		math.IsNaN(*commonOptions.ScoreThreshold) ||
		math.IsInf(*commonOptions.ScoreThreshold, 0) {
		return nil, errors.New("knowledge_retriever_invalid_score_threshold")
	}

	metadata, err := mergeMetadataFilter(retriever.config.Metadata, commonOptions.DSLInfo)
	if err != nil {
		return nil, err
	}
	results, err := retriever.service.Retrieve(ctx, knowledgeretrieve.Request{
		KnowledgeBaseID:   retriever.config.KnowledgeBaseID,
		Query:             query,
		Metadata:          metadata,
		Limit:             *commonOptions.TopK,
		MinimumSimilarity: *commonOptions.ScoreThreshold,
	})
	if err != nil {
		return nil, err
	}

	documents := make([]*schema.Document, len(results))
	for index, result := range results {
		metadata := cloneMetadata(result.Metadata)
		metadata[MetadataKnowledgeBaseID] = retriever.config.KnowledgeBaseID
		metadata[MetadataDocumentID] = result.DocumentID
		metadata[MetadataVersionID] = result.VersionID
		metadata[MetadataDocumentType] = string(result.DocumentType)
		metadata[MetadataTitle] = result.Title
		metadata[MetadataRank] = result.Rank
		metadata[MetadataSimilarity] = result.Similarity
		documents[index] = (&schema.Document{
			ID:       result.ChunkID,
			Content:  result.Content,
			MetaData: metadata,
		}).WithScore(result.Similarity)
	}
	return documents, nil
}

// WithMetadataFilter 创建仅支持 JSON 包含语义的 Eino DSLInfo 选项。
func WithMetadataFilter(metadata map[string]any) einoretriever.Option {
	return einoretriever.WithDSLInfo(map[string]any{
		MetadataFilterKey: cloneMetadata(metadata),
	})
}

func mergeMetadataFilter(
	required map[string]any,
	dslInfo map[string]any,
) (map[string]any, error) {
	merged := cloneMetadata(required)
	if dslInfo == nil {
		return merged, nil
	}
	if len(dslInfo) != 1 {
		return nil, errors.New("knowledge_retriever_invalid_dsl")
	}
	rawMetadata, ok := dslInfo[MetadataFilterKey]
	if !ok {
		return nil, errors.New("knowledge_retriever_invalid_dsl")
	}
	metadata, ok := rawMetadata.(map[string]any)
	if !ok {
		return nil, errors.New("knowledge_retriever_invalid_metadata_filter")
	}
	metadata, err := normalizeMetadata(metadata)
	if err != nil {
		return nil, errors.New("knowledge_retriever_invalid_metadata_filter")
	}
	for key, value := range metadata {
		if requiredValue, exists := merged[key]; exists &&
			!reflect.DeepEqual(requiredValue, value) {
			return nil, errors.New("knowledge_retriever_required_filter_override_rejected")
		}
		merged[key] = value
	}
	return merged, nil
}

func cloneMetadata(metadata map[string]any) map[string]any {
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func normalizeMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}
