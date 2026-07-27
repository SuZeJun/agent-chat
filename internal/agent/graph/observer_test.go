package graph

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type recordingObserver struct {
	mutex      sync.Mutex
	evidence   [][]Evidence
	assessment []Assessment
	deltas     []string
}

func (observer *recordingObserver) OnRetrieval(_ context.Context, evidence []Evidence) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.evidence = append(observer.evidence, evidence)
}

func (observer *recordingObserver) OnAssessment(_ context.Context, assessment Assessment) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.assessment = append(observer.assessment, assessment)
}

func (observer *recordingObserver) OnAnswerDelta(_ context.Context, delta string) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.deltas = append(observer.deltas, delta)
}

func (observer *recordingObserver) joined() string {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return strings.Join(observer.deltas, "")
}

// TestObserverReceivesProgressBeforeCompletion 保证进度在 Run 结束前即可被观察到。
//
// 事件此前全部在终态一次性写入，客户端无法反映节点执行顺序；本测试锁定
// 检索、判定与回答增量都经由 Observer 在运行期发出。
func TestObserverReceivesProgressBeforeCompletion(t *testing.T) {
	answer := "请在设置页选择“重置密码”。[S1]"
	tests := []struct {
		name          string
		documents     []*schema.Document
		modelAnswer   string
		expectedTurns int
		expectedText  string
	}{
		{
			name:          "answerable streams multiple deltas",
			documents:     []*schema.Document{testDocument("chunk-1", 0.91, 1)},
			modelAnswer:   answer,
			expectedTurns: len(splitRunes(answer, fakeChunkRunes)),
			expectedText:  answer,
		},
		{
			name:          "clarification emits a single delta",
			documents:     []*schema.Document{testDocument("chunk-1", 0.6, 1)},
			expectedTurns: 1,
		},
		{
			name:          "refusal emits a single delta",
			documents:     []*schema.Document{testDocument("chunk-1", 0.3, 1)},
			expectedTurns: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retriever := &fakeRetriever{documents: test.documents}
			chatModel := &fakeChatModel{answer: test.modelAnswer}
			runtime := newTestRuntime(t, retriever, chatModel)
			observer := &recordingObserver{}

			ctx := WithObserver(context.Background(), observer)
			output, err := runtime.Run(ctx, Input{Query: "如何重置密码？"})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}

			if len(observer.evidence) != 1 {
				t.Fatalf("expected one retrieval report, got %d", len(observer.evidence))
			}
			if len(observer.assessment) != 1 {
				t.Fatalf("expected one assessment report, got %d", len(observer.assessment))
			}
			if observer.assessment[0].Decision != output.Assessment.Decision {
				t.Fatalf(
					"observed decision %q differs from result %q",
					observer.assessment[0].Decision,
					output.Assessment.Decision,
				)
			}
			if len(observer.deltas) != test.expectedTurns {
				t.Fatalf("expected %d deltas, got %d", test.expectedTurns, len(observer.deltas))
			}
			// 增量拼接必须等于最终回答，否则客户端累积出的内容与持久化结果不一致。
			if observer.joined() != output.Answer {
				t.Fatalf("joined deltas %q differ from answer %q", observer.joined(), output.Answer)
			}
			if test.expectedText != "" && output.Answer != test.expectedText {
				t.Fatalf("unexpected answer %q", output.Answer)
			}
		})
	}
}

// TestGraphRunsWithoutObserver 保证未绑定 Observer 时 Graph 仍然可用。
func TestGraphRunsWithoutObserver(t *testing.T) {
	retriever := &fakeRetriever{
		documents: []*schema.Document{testDocument("chunk-1", 0.91, 1)},
	}
	chatModel := &fakeChatModel{answer: "请在设置页选择“重置密码”。[S1]"}
	runtime := newTestRuntime(t, retriever, chatModel)

	output, err := runtime.Run(context.Background(), Input{Query: "如何重置密码？"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if output.Assessment.Decision != DecisionAnswerable || len(output.Citations) != 1 {
		t.Fatalf("unexpected output: %#v", output)
	}
}
