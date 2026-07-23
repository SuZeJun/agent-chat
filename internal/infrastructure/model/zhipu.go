package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-chat/internal/pkg/config"

	einoembedding "github.com/cloudwego/eino/components/embedding"
)

const (
	maxEmbeddingBatchSize       = 64
	maxEmbeddingResponseBytes   = 8 << 20
	maxEmbeddingErrorDrainBytes = 64 << 10
	zhipuEmbeddingProvider      = "zhipu"
)

var errEmbeddingResponseTooLarge = errors.New("embedding response exceeds size limit")

var _ einoembedding.Embedder = (*ZhipuEmbedder)(nil)

// ZhipuEmbedder 将智谱 OpenAI 兼容 Embeddings API 适配为 Eino Embedder。
//
// 实例固定模型和维度，确保文档索引与查询向量不会因调用级覆盖而不兼容。
type ZhipuEmbedder struct {
	apiKey     string
	endpoint   string
	model      string
	dimensions int
	client     *http.Client
}

// EmbeddingIdentity 唯一标识生成某批向量的 Provider、模型和维度。
//
// 后续索引版本必须持久化该身份，并在查询旧索引前做完整匹配；仅比较维度
// 无法阻止不同模型的向量空间被静默混用。
type EmbeddingIdentity struct {
	Provider   string
	Model      string
	Dimensions int
}

// NewZhipuEmbedder 根据配置创建智谱 Eino Embedder。
func NewZhipuEmbedder(cfg config.EmbeddingModel) (*ZhipuEmbedder, error) {
	return newZhipuEmbedder(cfg, &http.Client{Timeout: cfg.Timeout})
}

func newZhipuEmbedder(cfg config.EmbeddingModel, client *http.Client) (*ZhipuEmbedder, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_API_KEY is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_BASE_URL is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_MODEL is required")
	}
	if cfg.Model != "embedding-3" {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_MODEL must be embedding-3")
	}
	if cfg.Dimensions != 1024 {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_DIM must be 1024")
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("create Zhipu Embedder: EMBEDDING_TIMEOUT must be greater than zero")
	}
	if client == nil {
		return nil, fmt.Errorf("create Zhipu Embedder: HTTP client is required")
	}

	return &ZhipuEmbedder{
		apiKey:     cfg.APIKey,
		endpoint:   strings.TrimRight(cfg.BaseURL, "/") + "/embeddings",
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		client:     client,
	}, nil
}

// Dimensions 返回固定向量维度，供数据库 Schema 和索引构建执行一致性校验。
func (embedder *ZhipuEmbedder) Dimensions() int {
	return embedder.dimensions
}

// Identity 返回必须随索引版本持久化的稳定 embedding 身份。
func (embedder *ZhipuEmbedder) Identity() EmbeddingIdentity {
	return EmbeddingIdentity{
		Provider:   zhipuEmbeddingProvider,
		Model:      embedder.model,
		Dimensions: embedder.dimensions,
	}
}

// EmbedStrings 批量生成与输入顺序严格一致的向量。
func (embedder *ZhipuEmbedder) EmbedStrings(
	ctx context.Context,
	texts []string,
	options ...einoembedding.Option,
) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("embed strings: input must not be empty")
	}
	if len(texts) > maxEmbeddingBatchSize {
		return nil, fmt.Errorf("embed strings: batch size %d exceeds limit %d", len(texts), maxEmbeddingBatchSize)
	}
	for index, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("embed strings: input at index %d must not be blank", index)
		}
	}

	defaultModel := embedder.model
	commonOptions := einoembedding.GetCommonOptions(&einoembedding.Options{
		Model: &defaultModel,
	}, options...)
	if commonOptions.Model == nil || *commonOptions.Model != embedder.model {
		return nil, fmt.Errorf("embed strings: model override is not allowed")
	}

	payload, err := json.Marshal(embeddingRequest{
		Model:      embedder.model,
		Input:      texts,
		Dimensions: embedder.dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("embed strings: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, embedder.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embed strings: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+embedder.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := embedder.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("embed strings: call Zhipu API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxEmbeddingErrorDrainBytes))
		// 第三方错误正文可能回显请求内容或供应商内部信息，不向上层传播。
		return nil, fmt.Errorf("embed strings: Zhipu API returned status %d", response.StatusCode)
	}

	responseBody, err := readLimitedResponse(response.Body, maxEmbeddingResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("embed strings: read response: %w", err)
	}
	var decoded embeddingResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, fmt.Errorf("embed strings: decode response: %w", err)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf(
			"embed strings: response count mismatch: expected %d, got %d",
			len(texts),
			len(decoded.Data),
		)
	}

	embeddings := make([][]float64, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			return nil, fmt.Errorf("embed strings: response index %d is out of range", item.Index)
		}
		if embeddings[item.Index] != nil {
			return nil, fmt.Errorf("embed strings: response index %d is duplicated", item.Index)
		}
		if len(item.Embedding) != embedder.dimensions {
			return nil, fmt.Errorf(
				"embed strings: vector dimension mismatch at index %d: expected %d, got %d",
				item.Index,
				embedder.dimensions,
				len(item.Embedding),
			)
		}
		embeddings[item.Index] = item.Embedding
	}
	return embeddings, nil
}

func readLimitedResponse(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errEmbeddingResponseTooLarge
	}
	return body, nil
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

type embeddingResponse struct {
	Data []embeddingResponseItem `json:"data"`
}

type embeddingResponseItem struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}
