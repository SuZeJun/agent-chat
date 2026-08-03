package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	domain "agent-chat/internal/domain/ticket"
)

// TestDraftTicketToolOnlyProducesDraft 锁定该工具没有任何副作用。
//
// 工具名用 draft 而非 create 正是为了让这一点显而易见：写操作发生在客户确认
// 之后的独立任务里，而不是模型调用工具的瞬间。
func TestDraftTicketToolOnlyProducesDraft(t *testing.T) {
	tool := NewDraftTicketTool()
	arguments := `{"title":"无法导出账单","description":"点击导出按钮没有反应。","priority":"high"}`

	result, err := tool.InvokableRun(context.Background(), arguments)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var draft domain.Draft
	if err := json.Unmarshal([]byte(result), &draft); err != nil {
		t.Fatalf("tool result is not a valid draft: %v", err)
	}
	if draft.Title != "无法导出账单" ||
		draft.Priority != domain.PriorityHigh ||
		!strings.Contains(draft.Description, "没有反应") {
		t.Fatalf("unexpected draft: %#v", draft)
	}
}

// TestDraftTicketToolRejectsInvalidDrafts 保证残缺草稿无法进入确认界面。
//
// 确认界面是安全边界的一部分：展示一份字段残缺或优先级非法的草稿，等于让客户
// 在看不清内容的情况下授权写操作。
func TestDraftTicketToolRejectsInvalidDrafts(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		code      string
	}{
		{"空参数", "", "invalid_tool_arguments"},
		{"非 JSON", "not json", "invalid_tool_arguments"},
		{"缺标题", `{"description":"详细描述","priority":"low"}`, "invalid_ticket_draft"},
		{"缺描述", `{"title":"标题","priority":"low"}`, "invalid_ticket_draft"},
		{"空白标题", `{"title":"   ","description":"详细描述","priority":"low"}`, "invalid_ticket_draft"},
		{"非法优先级", `{"title":"标题","description":"详细描述","priority":"urgent"}`, "invalid_ticket_draft"},
		{"缺优先级", `{"title":"标题","description":"详细描述"}`, "invalid_ticket_draft"},
		{
			"超长标题",
			`{"title":"` + strings.Repeat("题", 121) + `","description":"详细描述","priority":"low"}`,
			"invalid_ticket_draft",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := NewDraftTicketTool()
			_, err := tool.InvokableRun(context.Background(), test.arguments)
			assertFailureCode(t, err, test.code, false)
		})
	}
}

func TestDraftTicketToolDeclaresConstrainedPriority(t *testing.T) {
	tool := NewDraftTicketTool()
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}
	if info.Name != DraftTicketToolName {
		t.Fatalf("unexpected tool name: %q", info.Name)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("draft tool must declare parameters")
	}
	// 优先级必须是受限枚举：模型自造的等级下游无法处理。
	params, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("convert params: %v", err)
	}
	priority, ok := params.Properties.Get("priority")
	if !ok {
		t.Fatal("priority parameter is missing")
	}
	if len(priority.Enum) != 3 {
		t.Fatalf("priority must be constrained to three levels: %#v", priority.Enum)
	}
}
