package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

func TestTraceStepNameAlwaysSatisfiesPersistenceContract(t *testing.T) {
	tests := []struct {
		name     string
		info     *callbacks.RunInfo
		expected string
	}{
		{
			name: "graph node keeps its own name",
			info: &callbacks.RunInfo{
				Name:      nodeGroundedGenerate,
				Component: compose.ComponentOfLambda,
			},
			expected: nodeGroundedGenerate,
		},
		{
			name: "component invoked inside a lambda falls back to its component",
			info: &callbacks.RunInfo{
				Name:      "",
				Type:      "OpenAI",
				Component: components.ComponentOfChatModel,
			},
			expected: string(components.ComponentOfChatModel),
		},
		{
			name: "blank name falls back instead of persisting whitespace",
			info: &callbacks.RunInfo{
				Name:      "   ",
				Component: components.ComponentOfChatModel,
			},
			expected: string(components.ComponentOfChatModel),
		},
		{
			name: "over-long name is truncated to the persisted limit",
			info: &callbacks.RunInfo{
				Name:      strings.Repeat("a", maxTraceNameBytes+20),
				Component: compose.ComponentOfLambda,
			},
			expected: strings.Repeat("a", maxTraceNameBytes),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := traceStepName(test.info)
			if name != test.expected {
				t.Fatalf("expected name %q, got %q", test.expected, name)
			}
			if strings.TrimSpace(name) == "" || len(name) > maxTraceNameBytes {
				t.Fatalf("name %q violates the persistence contract", name)
			}
		})
	}
}

func TestTruncateTraceFieldKeepsValidUTF8(t *testing.T) {
	truncated := truncateTraceField(strings.Repeat("知", 10), 8)
	if len(truncated) > 8 {
		t.Fatalf("expected at most 8 bytes, got %d", len(truncated))
	}
	if truncated != "知知" {
		t.Fatalf("expected whole runes only, got %q", truncated)
	}
}

// TestRealChatModelProducesPersistableTrace 使用真实 Eino ChatModel 组件执行完整
// Graph。测试替身不会触发 Eino Callback，因此只有真实组件才能覆盖到组件级 Trace：
// 该组件不是 Graph 节点，RunInfo.Name 为空，曾导致 Run 结果在提交阶段被拒绝。
func TestRealChatModelProducesPersistableTrace(t *testing.T) {
	// 受知识约束的回答走流式，因此这里必须返回 SSE 分块而非单个 JSON 响应。
	// Eino 会请求 stream_options.include_usage，用量随最后一个分块返回。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			return
		}
		for _, delta := range []string{"请在设置页", "点击重置密码。", "[S1]"} {
			fmt.Fprintf(writer, "data: {\"id\":\"trace-probe\",\"object\":\"chat.completion.chunk\","+
				"\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,"+
				"\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\n", delta)
			flusher.Flush()
		}
		fmt.Fprint(writer, "data: {\"id\":\"trace-probe\",\"object\":\"chat.completion.chunk\","+
			"\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,"+
			"\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,"+
			"\"completion_tokens\":20,\"total_tokens\":120}}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	ctx := context.Background()
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("create chat model: %v", err)
	}

	retriever := &fakeRetriever{
		documents: []*schema.Document{testDocument("chunk-1", 0.91, 1)},
	}
	runtime, err := NewRuntime(ctx, retriever, chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	output, err := runtime.Run(ctx, Input{Query: "如何重置密码？"})
	if err != nil {
		t.Fatalf("run graph: %v", err)
	}
	if len(output.Citations) == 0 {
		t.Fatal("expected the grounded answer to carry citations")
	}

	// 每个步骤都必须满足 Domain RunStepDraft 的持久化契约。
	for index, step := range output.Trace {
		if strings.TrimSpace(step.Name) == "" || len(step.Name) > maxTraceNameBytes {
			t.Fatalf("step %d has unpersistable name %q", index, step.Name)
		}
		if strings.TrimSpace(step.Component) == "" {
			t.Fatalf("step %d has empty component", index)
		}
		if len(step.ComponentType) > maxTraceComponentTypeBytes {
			t.Fatalf("step %d component type exceeds the persisted limit", index)
		}
	}

	var modelSteps int
	for _, step := range output.Trace {
		if step.Component != string(components.ComponentOfChatModel) {
			continue
		}
		modelSteps++
		if step.PromptTokens != 100 || step.CompletionTokens != 20 {
			t.Fatalf(
				"expected model token usage 100/20, got %d/%d",
				step.PromptTokens,
				step.CompletionTokens,
			)
		}
	}
	if modelSteps != 1 {
		t.Fatalf("expected exactly one ChatModel trace step, got %d", modelSteps)
	}
}
