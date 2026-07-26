package retrieval

import (
	"context"
	"errors"
	"math"
	"testing"

	"agent-chat/internal/application/knowledgeretrieve"
	domain "agent-chat/internal/domain/knowledge"

	"github.com/cloudwego/eino/components/embedding"
	einoretriever "github.com/cloudwego/eino/components/retriever"
)

type fakeService struct {
	request knowledgeretrieve.Request
	results []domain.SearchResult
	err     error
	calls   int
}

func (service *fakeService) Retrieve(
	_ context.Context,
	request knowledgeretrieve.Request,
) ([]domain.SearchResult, error) {
	service.calls++
	service.request = request
	return service.results, service.err
}

type optionEmbedder struct{}

func (optionEmbedder) EmbedStrings(
	context.Context,
	[]string,
	...embedding.Option,
) ([][]float64, error) {
	return nil, nil
}

func TestKnowledgeRetrieverMapsOptionsAndDocuments(t *testing.T) {
	service := &fakeService{results: []domain.SearchResult{testResult()}}
	retriever := newTestRetriever(t, service, Config{
		KnowledgeBaseID:       "base-1",
		DefaultTopK:           5,
		DefaultScoreThreshold: 0.6,
		Metadata:              map[string]any{"tenant_id": "tenant-1"},
	})

	documents, err := retriever.Retrieve(
		context.Background(),
		"如何重置密码？",
		einoretriever.WithIndex("base-1"),
		einoretriever.WithTopK(3),
		einoretriever.WithScoreThreshold(0.75),
		WithMetadataFilter(map[string]any{"locale": "zh-CN"}),
	)
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if service.request.KnowledgeBaseID != "base-1" ||
		service.request.Query != "如何重置密码？" ||
		service.request.Limit != 3 ||
		service.request.MinimumSimilarity != 0.75 ||
		service.request.Metadata["tenant_id"] != "tenant-1" ||
		service.request.Metadata["locale"] != "zh-CN" {
		t.Fatalf("unexpected application request: %#v", service.request)
	}
	if len(documents) != 1 {
		t.Fatalf("unexpected documents: %#v", documents)
	}
	document := documents[0]
	if document.ID != "chunk-1" ||
		document.Content != "请点击忘记密码。" ||
		document.Score() != 0.91 ||
		document.MetaData[MetadataDocumentID] != "document-1" ||
		document.MetaData[MetadataVersionID] != "version-1" ||
		document.MetaData[MetadataTitle] != "如何重置密码？" ||
		document.MetaData[MetadataRank] != 1 ||
		document.MetaData[MetadataSimilarity] != 0.91 {
		t.Fatalf("unexpected Eino document: %#v", document)
	}
}

func TestKnowledgeRetrieverUsesDefaultsAndReturnsEmptyResult(t *testing.T) {
	service := &fakeService{}
	retriever := newTestRetriever(t, service, Config{
		KnowledgeBaseID:       "base-1",
		DefaultTopK:           5,
		DefaultScoreThreshold: 0.6,
	})

	documents, err := retriever.Retrieve(context.Background(), "没有命中的问题")
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if len(documents) != 0 {
		t.Fatalf("unexpected documents: %#v", documents)
	}
	if service.request.Limit != 5 || service.request.MinimumSimilarity != 0.6 {
		t.Fatalf("defaults were not applied: %#v", service.request)
	}
}

func TestKnowledgeRetrieverRejectsUnsafeOptions(t *testing.T) {
	tests := []struct {
		name    string
		options []einoretriever.Option
	}{
		{name: "index override", options: []einoretriever.Option{
			einoretriever.WithIndex("base-2"),
		}},
		{name: "sub index", options: []einoretriever.Option{
			einoretriever.WithSubIndex("private"),
		}},
		{name: "embedding override", options: []einoretriever.Option{
			einoretriever.WithEmbedding(optionEmbedder{}),
		}},
		{name: "invalid top k", options: []einoretriever.Option{
			einoretriever.WithTopK(0),
		}},
		{name: "invalid threshold", options: []einoretriever.Option{
			einoretriever.WithScoreThreshold(2),
		}},
		{name: "non-finite threshold", options: []einoretriever.Option{
			einoretriever.WithScoreThreshold(math.NaN()),
		}},
		{name: "unknown DSL", options: []einoretriever.Option{
			einoretriever.WithDSLInfo(map[string]any{"sql": "SELECT *"}),
		}},
		{name: "required filter override", options: []einoretriever.Option{
			WithMetadataFilter(map[string]any{"tenant_id": "tenant-2"}),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{}
			retriever := newTestRetriever(t, service, Config{
				KnowledgeBaseID:       "base-1",
				DefaultTopK:           5,
				DefaultScoreThreshold: 0.6,
				Metadata:              map[string]any{"tenant_id": "tenant-1"},
			})
			if _, err := retriever.Retrieve(
				context.Background(),
				"问题",
				test.options...,
			); err == nil {
				t.Fatal("expected an error")
			}
			if service.calls != 0 {
				t.Fatal("unsafe options reached application service")
			}
		})
	}
}

func TestKnowledgeRetrieverDeepCopiesRequiredMetadata(t *testing.T) {
	required := map[string]any{
		"scope": map[string]any{"tenant_id": "tenant-1"},
	}
	service := &fakeService{}
	retriever := newTestRetriever(t, service, Config{
		KnowledgeBaseID:       "base-1",
		DefaultTopK:           5,
		DefaultScoreThreshold: 0.6,
		Metadata:              required,
	})
	required["scope"].(map[string]any)["tenant_id"] = "tenant-2"

	if _, err := retriever.Retrieve(context.Background(), "问题"); err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	scope := service.request.Metadata["scope"].(map[string]any)
	if scope["tenant_id"] != "tenant-1" {
		t.Fatalf("required metadata was mutated: %#v", service.request.Metadata)
	}
}

// TestKnowledgeRetrieverPropagatesSafeApplicationFailure 同时校验重试性可见性。
//
// RAG Graph 通过错误链上的 CanRetry 判定是否允许重试；只要该方法在链路上不可见，
// 可重试的检索失败就会被静默判成永久失败，Job 队列的有界重试永远不会生效。
func TestKnowledgeRetrieverPropagatesSafeApplicationFailure(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		name := map[bool]string{false: "permanent", true: "retryable"}[retryable]
		t.Run(name, func(t *testing.T) {
			service := &fakeService{
				err: &knowledgeretrieve.Failure{
					Code:         "query_embedding_failed",
					RetryAllowed: retryable,
				},
			}
			retriever := newTestRetriever(t, service, Config{
				KnowledgeBaseID:       "base-1",
				DefaultTopK:           5,
				DefaultScoreThreshold: 0.6,
			})

			_, err := retriever.Retrieve(context.Background(), "问题")
			var failure *knowledgeretrieve.Failure
			if !errors.As(err, &failure) || failure.Code != "query_embedding_failed" {
				t.Fatalf("unexpected error: %v", err)
			}
			var retryability interface{ CanRetry() bool }
			if !errors.As(err, &retryability) {
				t.Fatal("retrieval failure does not expose CanRetry to the Graph")
			}
			if retryability.CanRetry() != retryable {
				t.Fatalf("expected CanRetry %t, got %t", retryable, retryability.CanRetry())
			}
		})
	}
}

func newTestRetriever(
	t *testing.T,
	service Service,
	config Config,
) *KnowledgeRetriever {
	t.Helper()
	retriever, err := NewKnowledgeRetriever(service, config)
	if err != nil {
		t.Fatalf("NewKnowledgeRetriever returned error: %v", err)
	}
	return retriever
}

func testResult() domain.SearchResult {
	return domain.SearchResult{
		ChunkID:      "chunk-1",
		DocumentID:   "document-1",
		VersionID:    "version-1",
		DocumentType: domain.DocumentTypeFAQ,
		Title:        "如何重置密码？",
		Content:      "请点击忘记密码。",
		Metadata:     map[string]any{"locale": "zh-CN", "rank": "untrusted"},
		Similarity:   0.91,
		Rank:         1,
	}
}

var _ einoretriever.Retriever = (*KnowledgeRetriever)(nil)
