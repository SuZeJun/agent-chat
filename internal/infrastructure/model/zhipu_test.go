package model

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-chat/internal/pkg/config"

	einoembedding "github.com/cloudwego/eino/components/embedding"
)

func TestZhipuEmbedderReturnsVectorsInInputOrder(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("unexpected request method: %s", request.Method)
		}
		if request.URL.Path != "/embeddings" {
			t.Errorf("unexpected request path: %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authorization header")
		}
		var payload embeddingRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if payload.Model != "embedding-3" || payload.Dimensions != 256 {
			t.Errorf("unexpected request payload: %#v", payload)
		}
		firstVector := make([]float64, 256)
		firstVector[0] = 1
		secondVector := make([]float64, 256)
		secondVector[0] = 2
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(embeddingResponse{
			Data: []embeddingResponseItem{
				{Index: 1, Embedding: secondVector},
				{Index: 0, Embedding: firstVector},
			},
		}); err != nil {
			t.Errorf("encode response body: %v", err)
		}
	}))
	defer server.Close()

	embedder, err := newZhipuEmbedder(config.EmbeddingModel{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "embedding-3",
		Dimensions: 256,
		Timeout:    time.Second,
	}, server.Client())
	if err != nil {
		t.Fatalf("create embedder: %v", err)
	}

	embeddings, err := embedder.EmbedStrings(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed strings: %v", err)
	}
	if embeddings[0][0] != 1 || embeddings[1][0] != 2 {
		t.Fatalf("unexpected embedding order: %#v", embeddings)
	}
	if identity := embedder.Identity(); identity != (EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 256,
	}) {
		t.Fatalf("unexpected embedding identity: %#v", identity)
	}
}

func TestZhipuEmbedderRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	embedder := newTestZhipuEmbedder(nil)
	oversizedBatch := make([]string, maxEmbeddingBatchSize+1)
	tests := []struct {
		name    string
		texts   []string
		options []einoembedding.Option
	}{
		{name: "empty input"},
		{name: "blank input", texts: []string{" "}},
		{name: "oversized batch", texts: oversizedBatch},
		{
			name:    "model override",
			texts:   []string{"question"},
			options: []einoembedding.Option{einoembedding.WithModel("another-model")},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := embedder.EmbedStrings(context.Background(), test.texts, test.options...)
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestZhipuEmbedderRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		texts []string
		data  []embeddingResponseItem
	}{
		{
			name:  "count mismatch",
			texts: []string{"first", "second"},
			data:  []embeddingResponseItem{{Index: 0, Embedding: []float64{1, 1}}},
		},
		{
			name:  "duplicate index",
			texts: []string{"first", "second"},
			data: []embeddingResponseItem{
				{Index: 0, Embedding: []float64{1, 1}},
				{Index: 0, Embedding: []float64{2, 2}},
			},
		},
		{
			name:  "out of range index",
			texts: []string{"first"},
			data:  []embeddingResponseItem{{Index: 1, Embedding: []float64{1, 1}}},
		},
		{
			name:  "dimension mismatch",
			texts: []string{"first"},
			data:  []embeddingResponseItem{{Index: 0, Embedding: []float64{1}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(writer).Encode(embeddingResponse{Data: test.data}); err != nil {
					t.Errorf("encode response body: %v", err)
				}
			}))
			defer server.Close()

			embedder := newTestZhipuEmbedder(server.Client())
			embedder.endpoint = server.URL
			_, err := embedder.EmbedStrings(context.Background(), test.texts)
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestZhipuEmbedderLimitsResponseSize(t *testing.T) {
	t.Parallel()

	_, err := readLimitedResponse(strings.NewReader("12345"), 4)
	if !errors.Is(err, errEmbeddingResponseTooLarge) {
		t.Fatalf("expected response size error, got %v", err)
	}
}

func TestZhipuEmbedderDrainsErrorResponseForConnectionReuse(t *testing.T) {
	t.Parallel()

	const secret = "sensitive-provider-response"
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, secret, http.StatusTooManyRequests)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	embedder := newTestZhipuEmbedder(server.Client())
	embedder.endpoint = server.URL
	for range 2 {
		_, err := embedder.EmbedStrings(context.Background(), []string{"question"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed provider response: %v", err)
		}
	}
	if newConnections.Load() != 1 {
		t.Fatalf("expected one reused connection, got %d", newConnections.Load())
	}
}

func TestZhipuEmbedderRejectsUnsupportedModel(t *testing.T) {
	t.Parallel()

	_, err := NewZhipuEmbedder(config.EmbeddingModel{
		APIKey:     "test-key",
		BaseURL:    "https://open.bigmodel.cn/api/paas/v4",
		Model:      "embedding-2",
		Dimensions: 1024,
		Timeout:    time.Second,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestZhipuEmbedderRequiresAPIKey(t *testing.T) {
	t.Parallel()

	_, err := NewZhipuEmbedder(config.EmbeddingModel{
		BaseURL:    "https://open.bigmodel.cn/api/paas/v4",
		Model:      "embedding-3",
		Dimensions: 1024,
		Timeout:    time.Second,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func newTestZhipuEmbedder(client *http.Client) *ZhipuEmbedder {
	if client == nil {
		client = http.DefaultClient
	}
	return &ZhipuEmbedder{
		apiKey:     "test-key",
		endpoint:   "https://embedding.example.com/v1/embeddings",
		model:      "embedding-3",
		dimensions: 2,
		client:     client,
	}
}
