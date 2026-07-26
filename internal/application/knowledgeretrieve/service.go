package knowledgeretrieve

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	domain "agent-chat/internal/domain/knowledge"
)

const maxQueryRunes = 8000

// Repository 定义在线知识检索所需的最小持久化能力。
type Repository interface {
	SearchActiveChunks(context.Context, domain.SearchQuery) ([]domain.SearchResult, error)
}

// Embedder 定义查询向量化所需的模型无关能力。
type Embedder interface {
	Embed(context.Context, []string) ([][]float64, error)
	Identity() domain.EmbeddingIdentity
}

// Request 是经过服务端资源绑定后的知识检索请求。
type Request struct {
	KnowledgeBaseID   string
	Query             string
	Metadata          map[string]any
	Limit             int
	MinimumSimilarity float64
}

// Failure 是可安全跨 Application 边界传递的检索失败。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回不包含用户问题、模型响应或数据库细节的稳定错误码。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅供进程内错误判断使用，不应记录底层 cause。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// CanRetry 返回调用方是否可以安全重试本次检索操作。
//
// RAG Graph 通过错误链上的该方法判定重试性；缺少此方法时可重试的检索失败会被
// 静默降级为永久失败，Job 队列的有界重试也就永远不会生效。
func (failure *Failure) CanRetry() bool {
	return failure.RetryAllowed
}

// Service 负责问题向量化、向量空间校验和活动切片检索。
type Service struct {
	repository Repository
	embedder   Embedder
}

// NewService 创建在线知识检索服务。
func NewService(repository Repository, embedder Embedder) (*Service, error) {
	if repository == nil {
		return nil, errors.New("knowledge retrieval repository is required")
	}
	if embedder == nil {
		return nil, errors.New("knowledge retrieval embedder is required")
	}
	return &Service{repository: repository, embedder: embedder}, nil
}

// Retrieve 返回按相关度降序排列的活动知识切片；无命中是成功的空结果。
func (service *Service) Retrieve(
	ctx context.Context,
	request Request,
) ([]domain.SearchResult, error) {
	if err := validateRequest(request); err != nil {
		return nil, newFailure("invalid_retrieval_request", false, err)
	}
	identity := service.embedder.Identity()
	if err := identity.Validate(); err != nil {
		return nil, newFailure("invalid_embedding_identity", false, err)
	}

	vectors, err := service.embedder.Embed(ctx, []string{strings.TrimSpace(request.Query)})
	if err != nil {
		retryable := true
		var retryability interface{ CanRetry() bool }
		if errors.As(err, &retryability) {
			retryable = retryability.CanRetry()
		}
		return nil, newFailure("query_embedding_failed", retryable, err)
	}
	if len(vectors) != 1 {
		return nil, newFailure(
			"invalid_query_embedding",
			true,
			errors.New("embedding count mismatch"),
		)
	}

	query := domain.SearchQuery{
		KnowledgeBaseID:   strings.TrimSpace(request.KnowledgeBaseID),
		EmbeddingIdentity: identity,
		Embedding:         vectors[0],
		Metadata:          cloneMetadata(request.Metadata),
		Limit:             request.Limit,
		MinimumSimilarity: request.MinimumSimilarity,
	}
	if err := query.Validate(); err != nil {
		return nil, newFailure("invalid_query_embedding", true, err)
	}
	results, err := service.repository.SearchActiveChunks(ctx, query)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrEmbeddingIdentityMismatch):
			return nil, newFailure("embedding_identity_mismatch", false, err)
		case errors.Is(err, domain.ErrNotFound),
			errors.Is(err, domain.ErrInvalidState),
			errors.Is(err, domain.ErrConflict):
			return nil, newFailure("knowledge_search_rejected", false, err)
		default:
			return nil, newFailure("knowledge_search_failed", true, err)
		}
	}
	for index, result := range results {
		if err := result.Validate(); err != nil {
			return nil, newFailure("invalid_retrieval_result", false, err)
		}
		if result.Rank != index+1 {
			return nil, newFailure(
				"invalid_retrieval_result",
				false,
				errors.New("result ranks must be contiguous"),
			)
		}
	}
	return results, nil
}

func validateRequest(request Request) error {
	knowledgeBaseID := strings.TrimSpace(request.KnowledgeBaseID)
	if knowledgeBaseID == "" || len(knowledgeBaseID) > 64 {
		return errors.New("knowledge base ID must be 1-64 characters")
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || utf8.RuneCountInString(query) > maxQueryRunes {
		return errors.New("query must be 1-8000 characters")
	}
	if request.Limit <= 0 || request.Limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if request.MinimumSimilarity < -1 || request.MinimumSimilarity > 1 ||
		math.IsNaN(request.MinimumSimilarity) ||
		math.IsInf(request.MinimumSimilarity, 0) {
		return errors.New("minimum similarity must be a finite value between -1 and 1")
	}
	encoded, err := json.Marshal(request.Metadata)
	if err != nil {
		return errors.New("metadata must be valid JSON")
	}
	if len(encoded) > 64<<10 {
		return errors.New("metadata exceeds size limit")
	}
	return nil
}

func newFailure(code string, retryable bool, cause error) error {
	return &Failure{
		Code:         code,
		RetryAllowed: retryable,
		cause:        cause,
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
