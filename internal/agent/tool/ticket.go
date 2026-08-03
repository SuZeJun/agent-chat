package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/ticket"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// DraftTicketToolName 是工单草稿工具的稳定名称。
//
// 名称刻意用 draft 而非 create：该工具不创建任何工单，只产出待客户确认的草稿。
// 叫 create_ticket 会让后续读代码的人以为写操作发生在这里。
const DraftTicketToolName = "draft_ticket"

const draftTicketToolDescription = "当客户明确要求创建技术支持工单、或问题无法通过知识库解决" +
	"需要转交人工处理时，生成工单草稿。该工具只生成草稿，不会创建工单；" +
	"工单必须经客户确认后才会真正创建。"

var _ einotool.InvokableTool = (*DraftTicketTool)(nil)

// DraftTicketTool 依据会话内容生成结构化工单草稿。
//
// 与订阅查询工具不同，这个工具接受模型提供的业务参数：草稿内容本就是模型的
// 职责。但工单归属仍由服务端在后续持久化时绑定，模型无法指定为谁建单。
type DraftTicketTool struct{}

// NewDraftTicketTool 创建工单草稿工具。
func NewDraftTicketTool() *DraftTicketTool {
	return &DraftTicketTool{}
}

// Info 声明草稿字段。优先级为受限枚举，避免模型自造无法处理的等级。
func (tool *DraftTicketTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: DraftTicketToolName,
		Desc: draftTicketToolDescription,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title": {
				Type:     schema.String,
				Desc:     "工单标题，简明概括问题，不超过 120 字",
				Required: true,
			},
			"description": {
				Type:     schema.String,
				Desc:     "问题描述，包含客户反馈的现象、已尝试的操作和期望结果",
				Required: true,
			},
			"priority": {
				Type:     schema.String,
				Desc:     "优先级",
				Enum:     []string{"low", "normal", "high"},
				Required: true,
			},
		}),
	}, nil
}

// draftArguments 是模型给出的草稿参数。
type draftArguments struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
}

// InvokableRun 校验并回显草稿，不产生任何副作用。
//
// 返回的是校验后的草稿而非确认提示语：确认界面的措辞属于展示层，工具只负责
// 保证草稿字段合法。
func (tool *DraftTicketTool) InvokableRun(
	_ context.Context,
	argumentsInJSON string,
	_ ...einotool.Option,
) (string, error) {
	var arguments draftArguments
	trimmed := strings.TrimSpace(argumentsInJSON)
	if trimmed == "" {
		return "", NewFailure(
			DraftTicketToolName,
			"invalid_tool_arguments",
			false,
			errors.New("draft arguments are required"),
		)
	}
	if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
		return "", NewFailure(
			DraftTicketToolName,
			"invalid_tool_arguments",
			false,
			errors.New("arguments must be a JSON object"),
		)
	}

	draft := domain.Draft{
		Title:       strings.TrimSpace(arguments.Title),
		Description: strings.TrimSpace(arguments.Description),
		Priority:    domain.Priority(strings.TrimSpace(arguments.Priority)),
	}
	// 草稿要进入客户确认界面，字段不合法就不能继续：确认界面是安全边界的一部分，
	// 展示一份残缺的草稿等于让客户在看不清内容的情况下授权写操作。
	if err := draft.Validate(); err != nil {
		return "", NewFailure(DraftTicketToolName, "invalid_ticket_draft", false, err)
	}

	encoded, err := json.Marshal(draft)
	if err != nil {
		return "", NewFailure(DraftTicketToolName, "invalid_ticket_draft", false, err)
	}
	return string(encoded), nil
}
