package knowledgeindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-chat/internal/domain/knowledge"
)

const embeddingBatchSize = 64

// Repository 定义知识索引用例所需的最小持久化能力。
type Repository interface {
	LoadIndexSource(context.Context, string) (domain.IndexSource, error)
	ReplaceChunksAndMarkReady(
		context.Context,
		string,
		domain.EmbeddingIdentity,
		[]domain.Chunk,
		time.Time,
	) error
	MarkVersionFailed(context.Context, string, string) error
	PublishVersion(
		context.Context,
		string,
		string,
		domain.EmbeddingIdentity,
		time.Time,
	) error
}

// Embedder 定义索引用例使用的模型无关 embedding 能力。
type Embedder interface {
	Embed(context.Context, []string) ([][]float64, error)
	Identity() domain.EmbeddingIdentity
}

// Failure 是可跨 Application 边界安全传递的索引失败分类。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回可写入 Job 和版本状态的稳定错误码。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅供进程内错误判断使用，不应记录底层 cause。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// Indexer 编排一个知识版本的确定性切片、向量化、持久化和发布。
type Indexer struct {
	repository Repository
	embedder   Embedder
	chunker    Chunker
	now        func() time.Time
}

// NewIndexer 创建知识索引用例。
func NewIndexer(repository Repository, embedder Embedder, chunker Chunker) (*Indexer, error) {
	if repository == nil {
		return nil, errors.New("knowledge index repository is required")
	}
	if embedder == nil {
		return nil, errors.New("knowledge index embedder is required")
	}
	if chunker == nil {
		return nil, errors.New("knowledge index chunker is required")
	}
	return &Indexer{
		repository: repository,
		embedder:   embedder,
		chunker:    chunker,
		now:        time.Now,
	}, nil
}

// IndexVersion 索引并发布指定不可变版本，重复执行时保持幂等。
func (indexer *Indexer) IndexVersion(ctx context.Context, versionID string) error {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return newFailure("invalid_version_id", false, nil)
	}

	source, err := indexer.repository.LoadIndexSource(ctx, versionID)
	if err != nil {
		return classifyRepositoryFailure("index_source_load_failed", err)
	}
	identity := indexer.embedder.Identity()
	if err := identity.Validate(); err != nil {
		return indexer.failVersion(ctx, versionID, "invalid_embedding_identity", false, err)
	}
	if !source.Version.EmbeddingIdentity.Equal(identity) {
		return indexer.failVersion(
			ctx,
			versionID,
			"embedding_identity_mismatch",
			false,
			domain.ErrEmbeddingIdentityMismatch,
		)
	}
	if source.Active {
		return nil
	}
	if source.Status == domain.IndexStatusReady {
		return indexer.publish(ctx, source, identity)
	}

	drafts, err := indexer.chunker.Split(source)
	if err != nil {
		return indexer.failVersion(ctx, versionID, "document_chunking_failed", false, err)
	}
	embeddings, err := indexer.embed(ctx, drafts)
	if err != nil {
		retryable := true
		var retryability interface{ CanRetry() bool }
		if errors.As(err, &retryability) {
			retryable = retryability.CanRetry()
		}
		return indexer.failVersion(ctx, versionID, "embedding_failed", retryable, err)
	}

	chunks := make([]domain.Chunk, len(drafts))
	for position, draft := range drafts {
		chunk := domain.Chunk{
			ID:         chunkID(versionID, position),
			VersionID:  versionID,
			Position:   position,
			Content:    draft.Content,
			TokenCount: draft.TokenCount,
			Metadata:   draft.Metadata,
			Embedding:  embeddings[position],
		}
		if err := chunk.Validate(identity); err != nil {
			return indexer.failVersion(ctx, versionID, "invalid_embedding_response", true, err)
		}
		chunks[position] = chunk
	}

	if err := indexer.repository.ReplaceChunksAndMarkReady(
		ctx,
		versionID,
		identity,
		chunks,
		indexer.now(),
	); err != nil {
		if errors.Is(err, domain.ErrInvalidState) {
			latest, loadErr := indexer.repository.LoadIndexSource(ctx, versionID)
			if loadErr == nil && latest.Status == domain.IndexStatusReady {
				return indexer.publish(ctx, latest, identity)
			}
		}
		return indexer.failVersion(
			ctx,
			versionID,
			"knowledge_chunks_store_failed",
			repositoryErrorRetryable(err),
			err,
		)
	}
	return indexer.publish(ctx, source, identity)
}

func (indexer *Indexer) embed(
	ctx context.Context,
	drafts []ChunkDraft,
) ([][]float64, error) {
	embeddings := make([][]float64, 0, len(drafts))
	for start := 0; start < len(drafts); start += embeddingBatchSize {
		end := min(start+embeddingBatchSize, len(drafts))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = drafts[index].Content
		}
		batch, err := indexer.embedder.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		if len(batch) != len(texts) {
			return nil, errors.New("embedding count mismatch")
		}
		embeddings = append(embeddings, batch...)
	}
	return embeddings, nil
}

func (indexer *Indexer) publish(
	ctx context.Context,
	source domain.IndexSource,
	identity domain.EmbeddingIdentity,
) error {
	err := indexer.repository.PublishVersion(
		ctx,
		source.Document.ID,
		source.Version.ID,
		identity,
		indexer.now(),
	)
	if errors.Is(err, domain.ErrVersionSuperseded) {
		return nil
	}
	if err != nil {
		return classifyRepositoryFailure("knowledge_version_publish_failed", err)
	}
	return nil
}

func (indexer *Indexer) failVersion(
	ctx context.Context,
	versionID string,
	code string,
	retryable bool,
	cause error,
) error {
	if ctx.Err() == nil {
		if err := indexer.repository.MarkVersionFailed(ctx, versionID, code); err != nil &&
			!errors.Is(err, domain.ErrInvalidState) {
			return newFailure("knowledge_index_status_update_failed", true, err)
		}
	}
	return newFailure(code, retryable, cause)
}

func classifyRepositoryFailure(code string, err error) error {
	return newFailure(code, repositoryErrorRetryable(err), err)
}

func repositoryErrorRetryable(err error) bool {
	return !errors.Is(err, domain.ErrNotFound) &&
		!errors.Is(err, domain.ErrConflict) &&
		!errors.Is(err, domain.ErrInvalidState) &&
		!errors.Is(err, domain.ErrEmbeddingIdentityMismatch)
}

func newFailure(code string, retryable bool, cause error) error {
	return &Failure{
		Code:         code,
		RetryAllowed: retryable,
		cause:        cause,
	}
}

func chunkID(versionID string, position int) string {
	checksum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", versionID, position)))
	return "chk_" + hex.EncodeToString(checksum[:16])
}
