package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-chat/internal/pkg/config"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestDeepSeekChatModelSendsConfiguredRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		thinking         bool
		expectedThinking string
	}{
		{name: "disabled", thinking: false, expectedThinking: "disabled"},
		{name: "enabled", thinking: true, expectedThinking: "enabled"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost {
					t.Errorf("unexpected request method: %s", request.Method)
				}
				if request.URL.Path != "/chat/completions" {
					t.Errorf("unexpected request path: %s", request.URL.Path)
				}
				if request.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing authorization header")
				}
				if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{
					"id":"request-id",
					"object":"chat.completion",
					"created":1,
					"model":"deepseek-v4-flash",
					"choices":[{
						"index":0,
						"message":{"role":"assistant","content":"回答"},
						"finish_reason":"stop"
					}]
				}`))
			}))
			defer server.Close()

			chatModel, err := newDeepSeekChatModel(context.Background(), config.ChatModel{
				APIKey:   "test-key",
				BaseURL:  server.URL,
				Model:    "deepseek-v4-flash",
				Thinking: test.thinking,
				Timeout:  time.Second,
			}, server.Client())
			if err != nil {
				t.Fatalf("create chat model: %v", err)
			}
			response, err := chatModel.Generate(context.Background(), []*schema.Message{
				schema.UserMessage("问题"),
			})
			if err != nil {
				t.Fatalf("generate response: %v", err)
			}
			if response.Content != "回答" {
				t.Fatalf("unexpected response: %q", response.Content)
			}
			if requestBody["model"] != "deepseek-v4-flash" {
				t.Fatalf("unexpected model: %#v", requestBody["model"])
			}
			expectedThinking := map[string]any{"type": test.expectedThinking}
			if !reflect.DeepEqual(requestBody["thinking"], expectedThinking) {
				t.Fatalf("unexpected thinking field: %#v", requestBody["thinking"])
			}
		})
	}
}

func TestDeepSeekChatModelSanitizesProviderErrors(t *testing.T) {
	t.Parallel()

	const secret = "sensitive-provider-response"
	tests := []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		retryable   bool
	}{
		{
			name:        "json client error",
			statusCode:  http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"error":{"message":"` + secret + `","type":"invalid_request_error"}}`,
			retryable:   false,
		},
		{
			name:        "plain server error",
			statusCode:  http.StatusInternalServerError,
			contentType: "text/plain",
			body:        secret,
			retryable:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			chatModel, err := newDeepSeekChatModel(context.Background(), config.ChatModel{
				APIKey:  "test-key",
				BaseURL: server.URL,
				Model:   "deepseek-v4-flash",
				Timeout: time.Second,
			}, server.Client())
			if err != nil {
				t.Fatalf("create chat model: %v", err)
			}
			_, err = chatModel.Generate(context.Background(), []*schema.Message{
				schema.UserMessage("问题"),
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposed provider response: %v", err)
			}
			var providerError *ModelProviderError
			if !errors.As(err, &providerError) {
				t.Fatalf("expected ModelProviderError, got %T", err)
			}
			if providerError.StatusCode != test.statusCode || providerError.Retryable != test.retryable {
				t.Fatalf("unexpected provider error: %#v", providerError)
			}
		})
	}
}

func TestDeepSeekTransportSanitizesBodyBeforeEinoReadsIt(t *testing.T) {
	t.Parallel()

	const secret = "sensitive-provider-response"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(secret))
	}))
	defer server.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := sanitizedModelHTTPClient(time.Second, server.Client()).Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("transport exposed provider response: %s", body)
	}
	if string(body) != sanitizedProviderErrorBody {
		t.Fatalf("unexpected sanitized response: %s", body)
	}
}

func TestDeepSeekChatModelSanitizesStreamReceiveError(t *testing.T) {
	t.Parallel()

	const secret = "sensitive-stream-response"
	chatModel := &DeepSeekChatModel{
		inner: &streamErrorChatModel{err: fmt.Errorf("%s", secret)},
	}
	stream, err := chatModel.Stream(context.Background(), []*schema.Message{
		schema.UserMessage("问题"),
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	defer stream.Close()

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed stream response: %v", err)
	}
	var providerError *ModelProviderError
	if !errors.As(err, &providerError) {
		t.Fatalf("expected ModelProviderError, got %T", err)
	}
}

func TestDeepSeekChatModelRequiresAPIKey(t *testing.T) {
	t.Parallel()

	_, err := NewDeepSeekChatModel(context.Background(), config.ChatModel{
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-flash",
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestDeepSeekChatModelRejectsUnsupportedModel(t *testing.T) {
	t.Parallel()

	_, err := NewDeepSeekChatModel(context.Background(), config.ChatModel{
		APIKey:  "test-key",
		BaseURL: "https://api.deepseek.com",
		Model:   "unknown-model",
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

type streamErrorChatModel struct {
	err error
}

func (model *streamErrorChatModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return nil, model.err
}

func (model *streamErrorChatModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(nil, model.err)
	writer.Close()
	return reader, nil
}

func (model *streamErrorChatModel) WithTools(
	[]*schema.ToolInfo,
) (einomodel.ToolCallingChatModel, error) {
	return model, nil
}
