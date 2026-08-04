package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxHandoffReasonRunes = 4_000
	maxHandoffListItems   = 20
)

// ConversationEventType 是人工接管链路的持久化实时事件类型。
type ConversationEventType string

const (
	ConversationEventHandoffRequested ConversationEventType = "handoff.requested"
	ConversationEventTakenOver        ConversationEventType = "handoff.taken_over"
	ConversationEventCustomerMessage  ConversationEventType = "message.customer"
	ConversationEventAgentMessage     ConversationEventType = "message.agent"
	ConversationEventAIResumed        ConversationEventType = "handoff.ai_resumed"
)

// ConversationActorType 标识会话审计事件的操作者类别。
type ConversationActorType string

const (
	ConversationActorCustomer ConversationActorType = "customer"
	ConversationActorAgent    ConversationActorType = "agent"
	ConversationActorSystem   ConversationActorType = "system"
)

// HandoffCitation 是客服摘要中可核对的知识引用。
type HandoffCitation struct {
	SourceID   string `json:"sourceId"`
	Title      string `json:"title"`
	Excerpt    string `json:"excerpt"`
	DocumentID string `json:"documentId"`
	VersionID  string `json:"versionId"`
}

// HandoffToolCall 是客服摘要中脱敏后的工具执行记录。
type HandoffToolCall struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// HandoffContext 是生成接管摘要所需的持久化会话事实。
type HandoffContext struct {
	Reason    string
	Messages  []Message
	Citations []HandoffCitation
	ToolCalls []HandoffToolCall
}

// HandoffSummary 是客服接管前可恢复的结构化上下文。
type HandoffSummary struct {
	ConversationID      string            `json:"conversationId"`
	CustomerRequest     string            `json:"customerRequest"`
	ConfirmedFacts      []string          `json:"confirmedFacts"`
	UnresolvedQuestions []string          `json:"unresolvedQuestions"`
	RiskSignals         []string          `json:"riskSignals"`
	Citations           []HandoffCitation `json:"citations"`
	ToolCalls           []HandoffToolCall `json:"toolCalls"`
	RecommendedAction   string            `json:"recommendedAction"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
}

// BuildHandoffSummary 从持久化消息、引用和工具记录生成确定性摘要，不调用模型。
func BuildHandoffSummary(conversationID string, context HandoffContext, now time.Time) (HandoffSummary, error) {
	if err := validateID("conversation ID", conversationID); err != nil {
		return HandoffSummary{}, err
	}
	reason := strings.TrimSpace(context.Reason)
	if utf8.RuneCountInString(reason) > maxHandoffReasonRunes {
		return HandoffSummary{}, errors.New("handoff reason is too long")
	}
	latestCustomer := ""
	for _, message := range context.Messages {
		if err := message.Validate(); err != nil || message.ConversationID != conversationID {
			return HandoffSummary{}, errors.New("handoff context contains an invalid message")
		}
		if message.Role == MessageRoleCustomer {
			latestCustomer = strings.TrimSpace(message.Content)
		}
	}
	request := reason
	if request == "" {
		request = latestCustomer
	}
	if request == "" {
		request = "客户请求人工支持"
	}

	citations := uniqueCitations(context.Citations)
	// JSONB 约束要求列表始终编码为数组；空切片不能退化为 null。
	toolCalls := make([]HandoffToolCall, len(context.ToolCalls))
	copy(toolCalls, context.ToolCalls)
	if len(toolCalls) > maxHandoffListItems {
		toolCalls = toolCalls[len(toolCalls)-maxHandoffListItems:]
	}
	facts := make([]string, 0, min(len(citations), 5))
	for _, citation := range citations {
		fact := strings.TrimSpace(citation.Title)
		if excerpt := strings.TrimSpace(citation.Excerpt); excerpt != "" {
			fact += "：" + excerpt
		}
		if fact != "" {
			facts = append(facts, truncateRunes(fact, 500))
		}
		if len(facts) == 5 {
			break
		}
	}
	summary := HandoffSummary{
		ConversationID:      conversationID,
		CustomerRequest:     request,
		ConfirmedFacts:      facts,
		UnresolvedQuestions: []string{request},
		RiskSignals:         detectRiskSignals(context),
		Citations:           citations,
		ToolCalls:           toolCalls,
		RecommendedAction:   "核对客户诉求与引用事实，说明可执行的下一步；需要写操作时继续使用确认流程。",
		CreatedAt:           now.UTC(),
		UpdatedAt:           now.UTC(),
	}
	if err := summary.Validate(); err != nil {
		return HandoffSummary{}, err
	}
	return summary, nil
}

// Validate 校验摘要结构、大小和时间。
func (summary HandoffSummary) Validate() error {
	if err := validateID("conversation ID", summary.ConversationID); err != nil {
		return err
	}
	if strings.TrimSpace(summary.CustomerRequest) == "" || strings.TrimSpace(summary.RecommendedAction) == "" {
		return errors.New("handoff summary text is incomplete")
	}
	if summary.CreatedAt.IsZero() || summary.UpdatedAt.Before(summary.CreatedAt) {
		return errors.New("handoff summary timestamps are invalid")
	}
	for _, size := range []int{len(summary.ConfirmedFacts), len(summary.UnresolvedQuestions), len(summary.RiskSignals), len(summary.Citations), len(summary.ToolCalls)} {
		if size > maxHandoffListItems {
			return errors.New("handoff summary list is too large")
		}
	}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > 128<<10 {
		return errors.New("handoff summary exceeds size limit")
	}
	return nil
}

// ConversationEvent 是按会话 sequence 严格递增的状态与消息审计事件。
type ConversationEvent struct {
	ID             string
	ConversationID string
	Sequence       int
	Type           ConversationEventType
	ActorType      ConversationActorType
	ActorID        string
	Payload        map[string]any
	CreatedAt      time.Time
}

// Validate 校验实时事件的身份、类型和 JSON 边界。
func (event ConversationEvent) Validate() error {
	if err := validateID("conversation event ID", event.ID); err != nil {
		return err
	}
	if err := validateID("conversation ID", event.ConversationID); err != nil {
		return err
	}
	if event.Sequence <= 0 {
		return errors.New("conversation event sequence must be positive")
	}
	switch event.Type {
	case ConversationEventHandoffRequested, ConversationEventTakenOver,
		ConversationEventCustomerMessage, ConversationEventAgentMessage,
		ConversationEventAIResumed:
	default:
		return fmt.Errorf("invalid conversation event type %q", event.Type)
	}
	switch event.ActorType {
	case ConversationActorCustomer, ConversationActorAgent:
		if err := validateID("conversation event actor ID", event.ActorID); err != nil {
			return err
		}
	case ConversationActorSystem:
		if event.ActorID != "" {
			return errors.New("system conversation event must not contain actor ID")
		}
	default:
		return errors.New("conversation event actor type is invalid")
	}
	if event.Payload == nil {
		return errors.New("conversation event payload is required")
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil || len(encoded) > 64<<10 {
		return errors.New("conversation event payload is invalid")
	}
	if event.CreatedAt.IsZero() {
		return errors.New("conversation event timestamp is required")
	}
	return nil
}

// HandoffConversation 是客服队列与详情共用的会话快照。
type HandoffConversation struct {
	Conversation    Conversation
	AssignedAgentID string
	LastMessageAt   *time.Time
	Summary         HandoffSummary
	Messages        []Message
	Events          []ConversationEvent
}

// ConversationEventPage 是持久化会话事件的增量页。
type ConversationEventPage struct {
	ConversationID  string
	Status          ConversationStatus
	AssignedAgentID string
	Events          []ConversationEvent
}

func uniqueCitations(input []HandoffCitation) []HandoffCitation {
	result := make([]HandoffCitation, 0, min(len(input), maxHandoffListItems))
	seen := make(map[string]struct{}, len(input))
	for _, citation := range input {
		key := citation.SourceID + "\x00" + citation.DocumentID + "\x00" + citation.VersionID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		citation.Title = truncateRunes(strings.TrimSpace(citation.Title), 500)
		citation.Excerpt = truncateRunes(strings.TrimSpace(citation.Excerpt), 2_000)
		result = append(result, citation)
		if len(result) == maxHandoffListItems {
			break
		}
	}
	return result
}

func detectRiskSignals(context HandoffContext) []string {
	text := strings.ToLower(context.Reason)
	for _, message := range context.Messages {
		text += "\n" + strings.ToLower(message.Content)
	}
	candidates := []struct{ needle, label string }{
		{"投诉", "客户投诉"}, {"退款", "退款或账务风险"},
		{"泄露", "数据或安全风险"}, {"安全", "安全相关"},
		{"紧急", "紧急请求"}, {"sla", "SLA 风险"},
	}
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.Contains(text, candidate.needle) {
			result = append(result, candidate.label)
		}
	}
	return result
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
