package knowledgeretrieve

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	query   domain.SearchQuery
	results []domain.SearchResult
	err     error
	calls   int
	search  func(domain.SearchQuery) ([]domain.SearchResult, error)
}

func (repository *fakeRepository) SearchActiveChunks(
	_ context.Context,
	query domain.SearchQuery,
) ([]domain.SearchResult, error) {
	repository.calls++
	repository.query = query
	if repository.search != nil {
		return repository.search(query)
	}
	return repository.results, repository.err
}

type fakeEmbedder struct {
	identity domain.EmbeddingIdentity
	vectors  [][]float64
	err      error
	texts    []string
}

func (embedder *fakeEmbedder) Embed(
	_ context.Context,
	texts []string,
) ([][]float64, error) {
	embedder.texts = append([]string(nil), texts...)
	return embedder.vectors, embedder.err
}

func (embedder *fakeEmbedder) Identity() domain.EmbeddingIdentity {
	return embedder.identity
}

type retryableError struct {
	retryable bool
}

func (err retryableError) Error() string {
	return "sensitive provider detail"
}

func (err retryableError) CanRetry() bool {
	return err.retryable
}

//go:embed testdata/retrieval_cases.json
var retrievalCasesJSON []byte

func TestServiceRetrievalEvalCases(t *testing.T) {
	var cases []struct {
		Name                string         `json:"name"`
		Query               string         `json:"query"`
		Metadata            map[string]any `json:"metadata"`
		TopK                int            `json:"topK"`
		MinimumSimilarity   float64        `json:"minimumSimilarity"`
		ExpectedDocumentIDs []string       `json:"expectedDocumentIDs"`
	}
	if err := json.Unmarshal(retrievalCasesJSON, &cases); err != nil {
		t.Fatalf("decode retrieval eval cases: %v", err)
	}

	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			repository := &fakeRepository{
				search: func(query domain.SearchQuery) ([]domain.SearchResult, error) {
					if query.Metadata["locale"] != "zh-CN" {
						return nil, nil
					}
					result := testSearchResult(1)
					result.DocumentID = "document-password-reset"
					return []domain.SearchResult{result}, nil
				},
			}
			embedder := &fakeEmbedder{
				identity: testIdentity(),
				vectors:  [][]float64{testQueryVector()},
			}
			service := newTestService(t, repository, embedder)
			results, err := service.Retrieve(context.Background(), Request{
				KnowledgeBaseID:   "base-1",
				Query:             test.Query,
				Metadata:          test.Metadata,
				Limit:             test.TopK,
				MinimumSimilarity: test.MinimumSimilarity,
			})
			if err != nil {
				t.Fatalf("Retrieve returned error: %v", err)
			}
			documentIDs := make([]string, len(results))
			for index, result := range results {
				documentIDs[index] = result.DocumentID
			}
			if !slices.Equal(documentIDs, test.ExpectedDocumentIDs) {
				t.Fatalf(
					"unexpected documents: actual=%#v expected=%#v",
					documentIDs,
					test.ExpectedDocumentIDs,
				)
			}
		})
	}
}

func TestServiceEmbedsQueryAndSearchesActiveChunks(t *testing.T) {
	identity := testIdentity()
	expected := []domain.SearchResult{testSearchResult(1), testSearchResult(2)}
	repository := &fakeRepository{results: expected}
	embedder := &fakeEmbedder{
		identity: identity,
		vectors:  [][]float64{testQueryVector()},
	}
	service := newTestService(t, repository, embedder)
	metadata := map[string]any{"locale": "zh-CN"}

	results, err := service.Retrieve(context.Background(), Request{
		KnowledgeBaseID:   "base-1",
		Query:             "  如何重置密码？ ",
		Metadata:          metadata,
		Limit:             5,
		MinimumSimilarity: 0.7,
	})
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if !slices.Equal(embedder.texts, []string{"如何重置密码？"}) {
		t.Fatalf("unexpected embedding input: %#v", embedder.texts)
	}
	if repository.query.KnowledgeBaseID != "base-1" ||
		repository.query.Limit != 5 ||
		repository.query.MinimumSimilarity != 0.7 ||
		!repository.query.EmbeddingIdentity.Equal(identity) ||
		repository.query.Metadata["locale"] != "zh-CN" {
		t.Fatalf("unexpected search query: %#v", repository.query)
	}
	metadata["locale"] = "changed"
	if repository.query.Metadata["locale"] != "zh-CN" {
		t.Fatal("request metadata was not cloned")
	}
	if len(results) != len(expected) {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestServiceReturnsEmptyResultWithoutError(t *testing.T) {
	repository := &fakeRepository{}
	embedder := &fakeEmbedder{
		identity: testIdentity(),
		vectors:  [][]float64{testQueryVector()},
	}
	service := newTestService(t, repository, embedder)

	results, err := service.Retrieve(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestServiceRejectsInvalidRequestBeforeEmbedding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "blank knowledge base", mutate: func(request *Request) {
			request.KnowledgeBaseID = " "
		}},
		{name: "blank query", mutate: func(request *Request) {
			request.Query = " "
		}},
		{name: "invalid limit", mutate: func(request *Request) {
			request.Limit = 0
		}},
		{name: "invalid threshold", mutate: func(request *Request) {
			request.MinimumSimilarity = 2
		}},
		{name: "non-finite threshold", mutate: func(request *Request) {
			request.MinimumSimilarity = math.NaN()
		}},
		{name: "invalid metadata", mutate: func(request *Request) {
			request.Metadata = map[string]any{"invalid": make(chan int)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{}
			embedder := &fakeEmbedder{identity: testIdentity()}
			service := newTestService(t, repository, embedder)
			request := validRequest()
			test.mutate(&request)

			err := retrieveError(service, request)
			assertFailure(t, err, "invalid_retrieval_request", false)
			if len(embedder.texts) != 0 || repository.calls != 0 {
				t.Fatal("invalid request reached dependencies")
			}
		})
	}
}

func TestServiceClassifiesEmbeddingFailureWithoutExposingCause(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent", true: "retryable"}[retryable], func(t *testing.T) {
			repository := &fakeRepository{}
			embedder := &fakeEmbedder{
				identity: testIdentity(),
				err:      retryableError{retryable: retryable},
			}
			service := newTestService(t, repository, embedder)

			err := retrieveError(service, validRequest())
			assertFailure(t, err, "query_embedding_failed", retryable)
			if err.Error() == "sensitive provider detail" {
				t.Fatal("provider detail escaped retrieval boundary")
			}
			if repository.calls != 0 {
				t.Fatal("repository called after embedding failure")
			}
		})
	}
}

func TestServiceRejectsInvalidEmbeddingAndRepositoryResult(t *testing.T) {
	t.Run("embedding count", func(t *testing.T) {
		repository := &fakeRepository{}
		embedder := &fakeEmbedder{identity: testIdentity(), vectors: nil}
		service := newTestService(t, repository, embedder)
		assertFailure(t, retrieveError(service, validRequest()), "invalid_query_embedding", true)
	})

	t.Run("embedding dimensions", func(t *testing.T) {
		repository := &fakeRepository{}
		embedder := &fakeEmbedder{
			identity: testIdentity(),
			vectors:  [][]float64{{1}},
		}
		service := newTestService(t, repository, embedder)
		assertFailure(t, retrieveError(service, validRequest()), "invalid_query_embedding", true)
	})

	t.Run("result rank", func(t *testing.T) {
		result := testSearchResult(1)
		result.Rank = 2
		repository := &fakeRepository{results: []domain.SearchResult{result}}
		embedder := &fakeEmbedder{
			identity: testIdentity(),
			vectors:  [][]float64{testQueryVector()},
		}
		service := newTestService(t, repository, embedder)
		assertFailure(t, retrieveError(service, validRequest()), "invalid_retrieval_result", false)
	})
}

func TestServiceMapsEmbeddingIdentityMismatch(t *testing.T) {
	repository := &fakeRepository{err: domain.ErrEmbeddingIdentityMismatch}
	embedder := &fakeEmbedder{
		identity: testIdentity(),
		vectors:  [][]float64{testQueryVector()},
	}
	service := newTestService(t, repository, embedder)

	assertFailure(t, retrieveError(service, validRequest()), "embedding_identity_mismatch", false)
}

func newTestService(
	t *testing.T,
	repository Repository,
	embedder Embedder,
) *Service {
	t.Helper()
	service, err := NewService(repository, embedder)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	return service
}

func retrieveError(service *Service, request Request) error {
	_, err := service.Retrieve(context.Background(), request)
	return err
}

func assertFailure(
	t *testing.T,
	err error,
	expectedCode string,
	expectedRetryable bool,
) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected Failure, got %v", err)
	}
	if failure.Code != expectedCode || failure.RetryAllowed != expectedRetryable {
		t.Fatalf("unexpected failure: %#v", failure)
	}
	// 调用方通过错误链上的 CanRetry 读取重试性，而非直接访问字段；只断言字段
	// 无法发现该方法缺失，可重试失败会被静默降级为永久失败。
	var retryability interface{ CanRetry() bool }
	if !errors.As(err, &retryability) {
		t.Fatal("retrieval failure does not expose CanRetry to callers")
	}
	if retryability.CanRetry() != expectedRetryable {
		t.Fatalf("CanRetry disagrees with RetryAllowed: %#v", failure)
	}
}

func validRequest() Request {
	return Request{
		KnowledgeBaseID:   "base-1",
		Query:             "如何重置密码？",
		Metadata:          map[string]any{"locale": "zh-CN"},
		Limit:             5,
		MinimumSimilarity: 0.7,
	}
}

func testIdentity() domain.EmbeddingIdentity {
	return domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
}

func testQueryVector() []float64 {
	vector := make([]float64, 1024)
	vector[0] = 1
	return vector
}

func testSearchResult(rank int) domain.SearchResult {
	return domain.SearchResult{
		ChunkID:      "chunk-" + string(rune('0'+rank)),
		DocumentID:   "document-1",
		VersionID:    "version-1",
		DocumentType: domain.DocumentTypeFAQ,
		Title:        "如何重置密码？",
		Content:      "请点击忘记密码。",
		Metadata:     map[string]any{"locale": "zh-CN"},
		Similarity:   0.9 - float64(rank-1)*0.1,
		Rank:         rank,
	}
}
