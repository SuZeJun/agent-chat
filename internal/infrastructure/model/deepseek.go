package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-chat/internal/pkg/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	provideropenai "github.com/meguminnnnnnnnn/go-openai"
)

const (
	deepSeekProvider           = "deepseek"
	maxModelErrorDrainBytes    = 64 << 10
	sanitizedProviderErrorBody = `{"error":{"message":"model provider request failed","type":"provider_error"}}`
)

var errModelProviderTransport = errors.New("model provider transport failed")

var _ einomodel.ToolCallingChatModel = (*DeepSeekChatModel)(nil)

// ModelProviderError 是可安全写入日志和 Trace 的模型供应商错误。
//
// 原始供应商错误不会被包装或暴露，调用方只能依据状态码和 Retryable
// 决定重试、降级或返回稳定业务错误。
type ModelProviderError struct {
	Provider   string
	Operation  string
	StatusCode int
	Retryable  bool
}

// Error 返回不包含供应商响应正文和用户输入的稳定错误描述。
func (err *ModelProviderError) Error() string {
	if err.StatusCode > 0 {
		return fmt.Sprintf(
			"%s model %s failed: status=%d retryable=%t",
			err.Provider,
			err.Operation,
			err.StatusCode,
			err.Retryable,
		)
	}
	return fmt.Sprintf("%s model %s failed: retryable=%t", err.Provider, err.Operation, err.Retryable)
}

// CanRetry 返回调用方是否可以安全重试本次供应商操作。
func (err *ModelProviderError) CanRetry() bool {
	return err.Retryable
}

// DeepSeekChatModel 为 Eino ChatModel 增加供应商错误脱敏边界。
type DeepSeekChatModel struct {
	inner einomodel.ToolCallingChatModel
}

// NewDeepSeekChatModel 创建使用 OpenAI 兼容协议的 DeepSeek Eino ChatModel。
//
// thinking 参数必须显式传递，避免 DeepSeek 服务端默认值变化导致 FAQ 链路的
// 延迟和成本在未修改应用配置时发生漂移。
func NewDeepSeekChatModel(ctx context.Context, cfg config.ChatModel) (einomodel.ToolCallingChatModel, error) {
	return newDeepSeekChatModel(ctx, cfg, nil)
}

func newDeepSeekChatModel(
	ctx context.Context,
	cfg config.ChatModel,
	httpClient *http.Client,
) (einomodel.ToolCallingChatModel, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("create DeepSeek ChatModel: LLM_API_KEY is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("create DeepSeek ChatModel: LLM_BASE_URL is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("create DeepSeek ChatModel: LLM_MODEL is required")
	}
	switch cfg.Model {
	case "deepseek-v4-flash", "deepseek-v4-pro":
	default:
		return nil, fmt.Errorf("create DeepSeek ChatModel: unsupported LLM_MODEL %q", cfg.Model)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("create DeepSeek ChatModel: LLM_TIMEOUT must be greater than zero")
	}

	thinkingType := "disabled"
	if cfg.Thinking {
		thinkingType = "enabled"
	}
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		BaseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		Model:      cfg.Model,
		Timeout:    cfg.Timeout,
		HTTPClient: sanitizedModelHTTPClient(cfg.Timeout, httpClient),
		ExtraFields: map[string]any{
			"thinking": map[string]any{
				"type": thinkingType,
			},
		},
	})
	if err != nil {
		return nil, sanitizeModelProviderError("create", err)
	}
	return &DeepSeekChatModel{inner: chatModel}, nil
}

// Generate 生成完整回复，并在错误离开 Provider 边界前完成脱敏。
func (chatModel *DeepSeekChatModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	options ...einomodel.Option,
) (*schema.Message, error) {
	message, err := chatModel.inner.Generate(ctx, input, options...)
	if err != nil {
		return nil, sanitizeModelProviderError("generate", err)
	}
	return message, nil
}

// Stream 创建流式回复，并同时脱敏建连错误和后续 Recv 错误。
func (chatModel *DeepSeekChatModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	stream, err := chatModel.inner.Stream(ctx, input, options...)
	if err != nil {
		return nil, sanitizeModelProviderError("stream", err)
	}
	return schema.StreamReaderWithConvert(
		stream,
		func(message *schema.Message) (*schema.Message, error) {
			return message, nil
		},
		schema.WithErrWrapper(func(err error) error {
			return sanitizeModelProviderError("stream receive", err)
		}),
	), nil
}

// WithTools 返回带工具定义的新实例，不修改可被并发请求共享的当前实例。
func (chatModel *DeepSeekChatModel) WithTools(
	tools []*schema.ToolInfo,
) (einomodel.ToolCallingChatModel, error) {
	modelWithTools, err := chatModel.inner.WithTools(tools)
	if err != nil {
		return nil, sanitizeModelProviderError("bind tools", err)
	}
	return &DeepSeekChatModel{inner: modelWithTools}, nil
}

func sanitizeModelProviderError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	statusCode := modelProviderStatusCode(err)
	return &ModelProviderError{
		Provider:   deepSeekProvider,
		Operation:  operation,
		StatusCode: statusCode,
		Retryable: statusCode == 0 || statusCode == http.StatusRequestTimeout ||
			statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError,
	}
}

func modelProviderStatusCode(err error) int {
	var einoAPIError *einoopenai.APIError
	if errors.As(err, &einoAPIError) {
		return einoAPIError.HTTPStatusCode
	}
	var apiError *provideropenai.APIError
	if errors.As(err, &apiError) {
		return apiError.HTTPStatusCode
	}
	var requestError *provideropenai.RequestError
	if errors.As(err, &requestError) {
		return requestError.HTTPStatusCode
	}
	return 0
}

// sanitizedModelHTTPClient 在 Eino SDK 和 Callback 解析响应前移除供应商错误正文。
func sanitizedModelHTTPClient(timeout time.Duration, source *http.Client) *http.Client {
	client := &http.Client{Timeout: timeout}
	if source != nil {
		cloned := *source
		client = &cloned
		client.Timeout = timeout
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &sanitizedModelTransport{base: transport}
	return client
}

type sanitizedModelTransport struct {
	base http.RoundTripper
}

func (transport *sanitizedModelTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, errModelProviderTransport
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxModelErrorDrainBytes))
	_ = response.Body.Close()
	response.Body = io.NopCloser(strings.NewReader(sanitizedProviderErrorBody))
	response.ContentLength = int64(len(sanitizedProviderErrorBody))
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("Content-Length", fmt.Sprintf("%d", response.ContentLength))
	return response, nil
}
