package knowledgeindex

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	source          domain.IndexSource
	loadError       error
	replaceError    error
	publishError    error
	markError       error
	replacedChunks  []domain.Chunk
	published       int
	failureReasons  []string
	loadedVersionID string
}

func (repository *fakeRepository) LoadIndexSource(
	_ context.Context,
	versionID string,
) (domain.IndexSource, error) {
	repository.loadedVersionID = versionID
	return repository.source, repository.loadError
}

func (repository *fakeRepository) ReplaceChunksAndMarkReady(
	_ context.Context,
	_ string,
	_ domain.EmbeddingIdentity,
	chunks []domain.Chunk,
	_ time.Time,
) error {
	repository.replacedChunks = chunks
	if repository.replaceError == nil {
		repository.source.Status = domain.IndexStatusReady
	}
	return repository.replaceError
}

func (repository *fakeRepository) MarkVersionFailed(
	_ context.Context,
	_ string,
	reason string,
) error {
	repository.failureReasons = append(repository.failureReasons, reason)
	return repository.markError
}

func (repository *fakeRepository) PublishVersion(
	context.Context,
	string,
	string,
	domain.EmbeddingIdentity,
	time.Time,
) error {
	repository.published++
	return repository.publishError
}

type fakeEmbedder struct {
	identity   domain.EmbeddingIdentity
	err        error
	batchSizes []int
}

func (embedder *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	embedder.batchSizes = append(embedder.batchSizes, len(texts))
	if embedder.err != nil {
		return nil, embedder.err
	}
	vectors := make([][]float64, len(texts))
	for index := range texts {
		vector := make([]float64, embedder.identity.Dimensions)
		vector[index%len(vector)] = 1
		vectors[index] = vector
	}
	return vectors, nil
}

func (embedder *fakeEmbedder) Identity() domain.EmbeddingIdentity {
	return embedder.identity
}

type staticChunker struct {
	drafts []ChunkDraft
	err    error
}

func (chunker staticChunker) Split(domain.IndexSource) ([]ChunkDraft, error) {
	return chunker.drafts, chunker.err
}

type classifiedError struct {
	retryable bool
}

func (err classifiedError) Error() string {
	return "provider detail"
}

func (err classifiedError) CanRetry() bool {
	return err.retryable
}

func TestIndexerIndexesAndPublishesVersion(t *testing.T) {
	source := testIndexSource(domain.DocumentTypeFAQ, "如何重置密码？", "请点击忘记密码。")
	repository := &fakeRepository{source: source}
	embedder := &fakeEmbedder{identity: source.Version.EmbeddingIdentity}
	indexer := newTestIndexer(t, repository, embedder, NewDeterministicChunker())

	if err := indexer.IndexVersion(context.Background(), source.Version.ID); err != nil {
		t.Fatalf("IndexVersion returned error: %v", err)
	}
	if repository.loadedVersionID != source.Version.ID {
		t.Fatalf("unexpected loaded version: %s", repository.loadedVersionID)
	}
	if len(repository.replacedChunks) != 1 {
		t.Fatalf("unexpected chunks: %#v", repository.replacedChunks)
	}
	chunk := repository.replacedChunks[0]
	if chunk.Content != "问题：如何重置密码？\n答案：请点击忘记密码。" ||
		chunk.ID == "" ||
		len(chunk.Embedding) != 1024 {
		t.Fatalf("unexpected indexed chunk: %#v", chunk)
	}
	if repository.published != 1 {
		t.Fatalf("unexpected publish count: %d", repository.published)
	}
}

func TestIndexerSkipsEmbeddingForActiveOrReadyVersion(t *testing.T) {
	tests := []struct {
		name            string
		active          bool
		expectedPublish int
	}{
		{name: "active", active: true, expectedPublish: 0},
		{name: "ready but inactive", active: false, expectedPublish: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := testIndexSource(domain.DocumentTypeFAQ, "问题", "答案")
			source.Status = domain.IndexStatusReady
			source.Active = test.active
			repository := &fakeRepository{source: source}
			embedder := &fakeEmbedder{identity: source.Version.EmbeddingIdentity}
			indexer := newTestIndexer(t, repository, embedder, NewDeterministicChunker())

			if err := indexer.IndexVersion(context.Background(), source.Version.ID); err != nil {
				t.Fatalf("IndexVersion returned error: %v", err)
			}
			if len(embedder.batchSizes) != 0 {
				t.Fatalf("ready version was embedded again: %#v", embedder.batchSizes)
			}
			if repository.published != test.expectedPublish {
				t.Fatalf("unexpected publish count: %d", repository.published)
			}
		})
	}
}

func TestIndexerBatchesEmbeddingRequests(t *testing.T) {
	source := testIndexSource(domain.DocumentTypeMarkdown, "批量", "内容")
	repository := &fakeRepository{source: source}
	embedder := &fakeEmbedder{identity: source.Version.EmbeddingIdentity}
	drafts := make([]ChunkDraft, 65)
	for index := range drafts {
		drafts[index] = ChunkDraft{Content: "内容", TokenCount: 2}
	}
	indexer := newTestIndexer(t, repository, embedder, staticChunker{drafts: drafts})

	if err := indexer.IndexVersion(context.Background(), source.Version.ID); err != nil {
		t.Fatalf("IndexVersion returned error: %v", err)
	}
	if !slices.Equal(embedder.batchSizes, []int{64, 1}) {
		t.Fatalf("unexpected embedding batches: %#v", embedder.batchSizes)
	}
	if len(repository.replacedChunks) != 65 {
		t.Fatalf("unexpected stored chunk count: %d", len(repository.replacedChunks))
	}
}

func TestIndexerClassifiesFailuresAndPersistsSafeReason(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*fakeRepository, *fakeEmbedder)
		expectedCode  string
		retryable     bool
		expectedMark  string
		expectedCalls int
	}{
		{
			name: "identity mismatch",
			configure: func(_ *fakeRepository, embedder *fakeEmbedder) {
				embedder.identity.Model = "other-model"
			},
			expectedCode:  "embedding_identity_mismatch",
			expectedMark:  "embedding_identity_mismatch",
			expectedCalls: 1,
		},
		{
			name: "retryable provider failure",
			configure: func(_ *fakeRepository, embedder *fakeEmbedder) {
				embedder.err = classifiedError{retryable: true}
			},
			expectedCode:  "embedding_failed",
			retryable:     true,
			expectedMark:  "embedding_failed",
			expectedCalls: 1,
		},
		{
			name: "permanent provider failure",
			configure: func(_ *fakeRepository, embedder *fakeEmbedder) {
				embedder.err = classifiedError{retryable: false}
			},
			expectedCode:  "embedding_failed",
			expectedMark:  "embedding_failed",
			expectedCalls: 1,
		},
		{
			name: "source not found",
			configure: func(repository *fakeRepository, _ *fakeEmbedder) {
				repository.loadError = domain.ErrNotFound
			},
			expectedCode: "index_source_load_failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := testIndexSource(domain.DocumentTypeFAQ, "问题", "答案")
			repository := &fakeRepository{source: source}
			embedder := &fakeEmbedder{identity: source.Version.EmbeddingIdentity}
			test.configure(repository, embedder)
			indexer := newTestIndexer(t, repository, embedder, NewDeterministicChunker())

			err := indexer.IndexVersion(context.Background(), source.Version.ID)
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("expected Failure, got %v", err)
			}
			if failure.Code != test.expectedCode || failure.RetryAllowed != test.retryable {
				t.Fatalf("unexpected failure: %#v", failure)
			}
			if len(repository.failureReasons) != test.expectedCalls {
				t.Fatalf("unexpected failure marks: %#v", repository.failureReasons)
			}
			if test.expectedCalls > 0 && repository.failureReasons[0] != test.expectedMark {
				t.Fatalf("unexpected persisted reason: %q", repository.failureReasons[0])
			}
		})
	}
}

func TestIndexerTreatsSupersededPublishAsSuccess(t *testing.T) {
	source := testIndexSource(domain.DocumentTypeFAQ, "问题", "答案")
	source.Status = domain.IndexStatusReady
	repository := &fakeRepository{
		source:       source,
		publishError: domain.ErrVersionSuperseded,
	}
	embedder := &fakeEmbedder{identity: source.Version.EmbeddingIdentity}
	indexer := newTestIndexer(t, repository, embedder, NewDeterministicChunker())

	if err := indexer.IndexVersion(context.Background(), source.Version.ID); err != nil {
		t.Fatalf("superseded version should be successful, got %v", err)
	}
}

func newTestIndexer(
	t *testing.T,
	repository Repository,
	embedder Embedder,
	chunker Chunker,
) *Indexer {
	t.Helper()
	indexer, err := NewIndexer(repository, embedder, chunker)
	if err != nil {
		t.Fatalf("NewIndexer returned error: %v", err)
	}
	indexer.now = func() time.Time {
		return time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	}
	return indexer
}
