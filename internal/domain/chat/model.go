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
	maxIDLength              = 64
	maxClientMessageIDLength = 100
	maxMessageRunes          = 16_000
)

var (
	// ErrNotFound 表示客户授权范围内不存在目标会话。
	ErrNotFound = errors.New("chat entity not found")
	// ErrConflict 表示聊天实体违反唯一性约束。
	ErrConflict = errors.New("chat entity conflict")
	// ErrInvalidState 表示聊天实体当前状态不允许目标操作。
	ErrInvalidState = errors.New("invalid chat state")
	// ErrIdempotencyConflict 表示同一客户端消息 ID 被用于不同内容。
	ErrIdempotencyConflict = errors.New("chat idempotency conflict")
	// ErrInvalidCommand 表示命令自身不满足领域契约；重试不会改变结果。
	ErrInvalidCommand = errors.New("invalid chat command")
)

// ConversationStatus 表示会话由 AI 或人工接待的状态。
type ConversationStatus string

const (
	// ConversationStatusAIActive 表示客户消息可以创建新的 Agent Run。
	ConversationStatusAIActive ConversationStatus = "ai_active"
	// ConversationStatusWaitingHuman 表示会话等待客服接入，AI 不再自动回复。
	ConversationStatusWaitingHuman ConversationStatus = "waiting_human"
	// ConversationStatusHumanActive 表示客服已接管，AI 不再自动回复。
	ConversationStatusHumanActive ConversationStatus = "human_active"
	// ConversationStatusClosed 表示会话已经结束。
	ConversationStatusClosed ConversationStatus = "closed"
)

// MessageRole 表示消息发送方。
type MessageRole string

const (
	// MessageRoleCustomer 表示客户发送的消息。
	MessageRoleCustomer MessageRole = "customer"
	// MessageRoleAssistant 表示 Agent 生成的消息。
	MessageRoleAssistant MessageRole = "assistant"
	// MessageRoleAgent 表示人工客服发送的消息。
	MessageRoleAgent MessageRole = "agent"
	// MessageRoleSystem 表示系统状态说明。
	MessageRoleSystem MessageRole = "system"
)

// RunStatus 表示 Agent Run 的持久化生命周期。
type RunStatus string

const (
	// RunStatusPending 表示 Run 已创建并等待 Worker。
	RunStatusPending RunStatus = "pending"
	// RunStatusRunning 表示 Worker 正在执行 Agent Graph。
	RunStatusRunning RunStatus = "running"
	// RunStatusCompleted 表示 Run 已成功产生最终结果。
	RunStatusCompleted RunStatus = "completed"
	// RunStatusFailed 表示 Run 已永久失败。
	RunStatusFailed RunStatus = "failed"
)

// AgentRunJobType 是持久化 Agent 执行任务的稳定类型。
const AgentRunJobType = "agent.run"

// EventType 表示 SSE 和 Trace 共用的持久化运行事件类型。
type EventType string

const (
	// EventTypeRunStarted 表示 Worker 开始执行 Run。
	EventTypeRunStarted EventType = "run.started"
	// EventTypeRunStatus 表示 Run 状态快照。
	EventTypeRunStatus EventType = "run.status"
	// EventTypeRetrievalCompleted 表示知识检索已经完成。
	EventTypeRetrievalCompleted EventType = "retrieval.completed"
	// EventTypeAnswerabilityDecided 表示 Answerability Gate 已产生决策。
	EventTypeAnswerabilityDecided EventType = "answerability.decided"
	// EventTypeMessageDelta 表示回答文本增量。
	EventTypeMessageDelta EventType = "message.delta"
	// EventTypeMessageCitation 表示回答引用。
	EventTypeMessageCitation EventType = "message.citation"
	// EventTypeRunCompleted 表示 Run 成功结束。
	EventTypeRunCompleted EventType = "run.completed"
	// EventTypeRunFailed 表示 Run 永久失败。
	EventTypeRunFailed EventType = "run.failed"
)

// Conversation 是绑定客户授权范围和知识库的会话。
type Conversation struct {
	ID              string
	CustomerID      string
	KnowledgeBaseID string
	Status          ConversationStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate 校验会话创建所需字段。
func (conversation Conversation) Validate() error {
	if err := validateID("conversation ID", conversation.ID); err != nil {
		return err
	}
	if err := validateID("customer ID", conversation.CustomerID); err != nil {
		return err
	}
	if err := validateID("knowledge base ID", conversation.KnowledgeBaseID); err != nil {
		return err
	}
	if !conversation.Status.Valid() {
		return fmt.Errorf("invalid conversation status %q", conversation.Status)
	}
	if conversation.CreatedAt.IsZero() || conversation.UpdatedAt.IsZero() {
		return errors.New("conversation timestamps are required")
	}
	if conversation.UpdatedAt.Before(conversation.CreatedAt) {
		return errors.New("conversation updated time precedes created time")
	}
	return nil
}

// Valid 判断会话状态是否属于已知集合。
func (status ConversationStatus) Valid() bool {
	switch status {
	case ConversationStatusAIActive,
		ConversationStatusWaitingHuman,
		ConversationStatusHumanActive,
		ConversationStatusClosed:
		return true
	default:
		return false
	}
}

// Message 是会话内不可变的消息记录。
type Message struct {
	ID              string
	ConversationID  string
	ClientMessageID string
	AgentRunID      string
	Role            MessageRole
	Content         string
	CreatedAt       time.Time
}

// Validate 校验消息身份、角色和内容约束。
func (message Message) Validate() error {
	if err := validateID("message ID", message.ID); err != nil {
		return err
	}
	if err := validateID("conversation ID", message.ConversationID); err != nil {
		return err
	}
	switch message.Role {
	case MessageRoleCustomer, MessageRoleAssistant, MessageRoleAgent, MessageRoleSystem:
	default:
		return fmt.Errorf("invalid message role %q", message.Role)
	}
	clientMessageID := strings.TrimSpace(message.ClientMessageID)
	if message.Role == MessageRoleCustomer {
		if clientMessageID == "" || len(clientMessageID) > maxClientMessageIDLength {
			return fmt.Errorf(
				"customer message client ID must be 1-%d characters",
				maxClientMessageIDLength,
			)
		}
	} else if clientMessageID != "" {
		return errors.New("non-customer message must not have a client message ID")
	}
	agentRunID := strings.TrimSpace(message.AgentRunID)
	if message.Role == MessageRoleAssistant {
		if err := validateID("assistant message agent run ID", agentRunID); err != nil {
			return err
		}
	} else if agentRunID != "" {
		return errors.New("non-assistant message must not have an agent run ID")
	}
	if strings.TrimSpace(message.Content) == "" {
		return errors.New("message content must not be blank")
	}
	if utf8.RuneCountInString(message.Content) > maxMessageRunes {
		return fmt.Errorf("message content must not exceed %d characters", maxMessageRunes)
	}
	if message.CreatedAt.IsZero() {
		return errors.New("message created time is required")
	}
	return nil
}

// AgentRun 是由一条客户消息唯一触发的 Agent 执行记录。
type AgentRun struct {
	ID              string
	RequestID       string
	ConversationID  string
	SourceMessageID string
	Status          RunStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate 校验新建 Agent Run 的来源和状态。
func (run AgentRun) Validate() error {
	if err := run.ValidateSnapshot(); err != nil {
		return err
	}
	if run.Status != RunStatusPending {
		return errors.New("new agent run must be pending")
	}
	return nil
}

// ValidateSnapshot 校验任意生命周期阶段的 Agent Run 基础字段。
func (run AgentRun) ValidateSnapshot() error {
	if err := validateID("agent run ID", run.ID); err != nil {
		return err
	}
	if err := validateID("request ID", run.RequestID); err != nil {
		return err
	}
	if err := validateID("conversation ID", run.ConversationID); err != nil {
		return err
	}
	if err := validateID("source message ID", run.SourceMessageID); err != nil {
		return err
	}
	if !run.Status.Valid() {
		return fmt.Errorf("invalid agent run status %q", run.Status)
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return errors.New("agent run timestamps are required")
	}
	if run.UpdatedAt.Before(run.CreatedAt) {
		return errors.New("agent run updated time precedes created time")
	}
	return nil
}

// Valid 判断 Run 状态是否属于已知集合。
func (status RunStatus) Valid() bool {
	switch status {
	case RunStatusPending, RunStatusRunning, RunStatusCompleted, RunStatusFailed:
		return true
	default:
		return false
	}
}

// RunEvent 是按 Run 内 sequence 严格递增的持久化事件。
type RunEvent struct {
	ID        string
	RunID     string
	Sequence  int
	Type      EventType
	Payload   map[string]any
	CreatedAt time.Time
}

// RunEventPage 是一次按 sequence 增量读取的事件页及当前 Run 状态。
type RunEventPage struct {
	RunID  string
	Status RunStatus
	Events []RunEvent
}

// Terminal 判断事件源是否已经不会再产生新事件。
func (page RunEventPage) Terminal() bool {
	return page.Status == RunStatusCompleted || page.Status == RunStatusFailed
}

// Validate 校验运行事件的类型、顺序和 JSON Payload。
func (event RunEvent) Validate() error {
	if err := validateID("run event ID", event.ID); err != nil {
		return err
	}
	if err := validateID("agent run ID", event.RunID); err != nil {
		return err
	}
	if event.Sequence <= 0 {
		return errors.New("run event sequence must be greater than zero")
	}
	switch event.Type {
	case EventTypeRunStarted,
		EventTypeRunStatus,
		EventTypeRetrievalCompleted,
		EventTypeAnswerabilityDecided,
		EventTypeMessageDelta,
		EventTypeMessageCitation,
		EventTypeRunCompleted,
		EventTypeRunFailed:
	default:
		return fmt.Errorf("invalid run event type %q", event.Type)
	}
	if event.Payload == nil {
		return errors.New("run event payload must be a JSON object")
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return errors.New("run event payload must be valid JSON")
	}
	if len(payload) > 64<<10 {
		return errors.New("run event payload exceeds 64 KiB")
	}
	if event.CreatedAt.IsZero() {
		return errors.New("run event created time is required")
	}
	return nil
}

// StartRunSubmission 是消息、Run、首事件和 Job 的原子写入契约。
type StartRunSubmission struct {
	CustomerID string
	Message    Message
	Run        AgentRun
	Event      RunEvent
	JobID      string
}

// Validate 校验原子提交内所有关联 ID 和首状态的一致性。
func (submission StartRunSubmission) Validate() error {
	if err := validateID("customer ID", submission.CustomerID); err != nil {
		return err
	}
	if err := submission.Message.Validate(); err != nil {
		return fmt.Errorf("invalid source message: %w", err)
	}
	if submission.Message.Role != MessageRoleCustomer {
		return errors.New("source message must be a customer message")
	}
	if err := submission.Run.Validate(); err != nil {
		return fmt.Errorf("invalid agent run: %w", err)
	}
	if submission.Run.ConversationID != submission.Message.ConversationID ||
		submission.Run.SourceMessageID != submission.Message.ID {
		return errors.New("agent run does not match source message")
	}
	if err := submission.Event.Validate(); err != nil {
		return fmt.Errorf("invalid initial run event: %w", err)
	}
	if submission.Event.RunID != submission.Run.ID ||
		submission.Event.Sequence != 1 ||
		submission.Event.Type != EventTypeRunStatus ||
		submission.Event.Payload["status"] != string(RunStatusPending) {
		return errors.New("initial run event must describe pending status at sequence one")
	}
	if err := validateID("agent run job ID", submission.JobID); err != nil {
		return err
	}
	return nil
}

// StartRunResult 返回首次创建或幂等重放对应的稳定实体。
type StartRunResult struct {
	Message   Message
	Run       AgentRun
	Duplicate bool
}

// RunSource 是 Worker 执行 Graph 所需的持久化快照。
type RunSource struct {
	Run     AgentRun
	Message Message
	// KnowledgeBaseID 与 CustomerID 是 Agent 执行时的授权作用域：
	// 前者限定可检索的知识，后者限定工具可读取的客户数据。
	KnowledgeBaseID string
	CustomerID      string
	Conversation    ConversationStatus
}

// Validate 校验 Run、来源消息和会话绑定关系。
func (source RunSource) Validate() error {
	if err := source.Run.ValidateSnapshot(); err != nil {
		return fmt.Errorf("invalid run source run: %w", err)
	}
	if err := source.Message.Validate(); err != nil {
		return fmt.Errorf("invalid run source message: %w", err)
	}
	if source.Message.Role != MessageRoleCustomer ||
		source.Run.ConversationID != source.Message.ConversationID ||
		source.Run.SourceMessageID != source.Message.ID {
		return errors.New("run source relationships are inconsistent")
	}
	if err := validateID("customer ID", source.CustomerID); err != nil {
		return fmt.Errorf("invalid run source customer: %w", err)
	}
	if err := validateID("knowledge base ID", source.KnowledgeBaseID); err != nil {
		return err
	}
	if !source.Conversation.Valid() {
		return fmt.Errorf("invalid run source conversation status %q", source.Conversation)
	}
	return nil
}

// Terminal 判断 Run 是否已经不可再次执行。
func (source RunSource) Terminal() bool {
	return source.Run.Status == RunStatusCompleted || source.Run.Status == RunStatusFailed
}

// EventDraft 是由 Repository 在 Run 行锁内分配 sequence 的事件草稿。
type EventDraft struct {
	ID        string
	Type      EventType
	Payload   map[string]any
	CreatedAt time.Time
}

// RunStepDraft 是由 Eino Callback 生成、待随 Run 终态原子持久化的 Trace。
type RunStepDraft struct {
	Name             string
	Component        string
	ComponentType    string
	Status           string
	StartedAt        time.Time
	CompletedAt      time.Time
	DurationMillis   int64
	PromptTokens     int
	CompletionTokens int
}

// RunStep 是已经持久化并带稳定顺序的 Trace 节点。
type RunStep struct {
	Order int
	RunStepDraft
}

// RunTraceSnapshot 是管理员查看一次 Agent Run 所需的脱敏 Trace。
type RunTraceSnapshot struct {
	RunID          string
	RequestID      string
	ConversationID string
	// Question 是触发本次 Run 的客户问题；脱离问题无法判断检索命中是否合理。
	Question    string
	Status      RunStatus
	Result      map[string]any
	ErrorCode   string
	Steps       []RunStep
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// Validate 校验 Trace 不包含负耗时、负 Token 或不完整节点身份。
func (step RunStepDraft) Validate() error {
	if strings.TrimSpace(step.Name) == "" || len(step.Name) > 100 {
		return errors.New("run step name must be 1-100 characters")
	}
	if strings.TrimSpace(step.Component) == "" || len(step.Component) > 100 {
		return errors.New("run step component must be 1-100 characters")
	}
	if len(step.ComponentType) > 255 {
		return errors.New("run step component type exceeds 255 characters")
	}
	if step.Status != "completed" && step.Status != "failed" {
		return errors.New("run step status is invalid")
	}
	if step.StartedAt.IsZero() || step.CompletedAt.IsZero() ||
		step.CompletedAt.Before(step.StartedAt) {
		return errors.New("run step timestamps are invalid")
	}
	if step.DurationMillis < 0 ||
		step.PromptTokens < 0 ||
		step.CompletionTokens < 0 {
		return errors.New("run step metrics must not be negative")
	}
	return nil
}

// Validate 校验事件草稿，但不要求调用方预先计算 sequence。
func (event EventDraft) Validate() error {
	candidate := RunEvent{
		ID:        event.ID,
		RunID:     "sequence-assigned-by-repository",
		Sequence:  1,
		Type:      event.Type,
		Payload:   event.Payload,
		CreatedAt: event.CreatedAt,
	}
	return candidate.Validate()
}

// BeginRunAttempt 是开始一次 Job 尝试的原子状态转换。
type BeginRunAttempt struct {
	RunID   string
	Attempt int
	Event   EventDraft
}

// Validate 校验开始事件和尝试次数。
func (command BeginRunAttempt) Validate() error {
	if err := validateID("agent run ID", command.RunID); err != nil {
		return err
	}
	if command.Attempt <= 0 {
		return errors.New("agent run attempt must be greater than zero")
	}
	if err := command.Event.Validate(); err != nil {
		return err
	}
	if command.Event.Type != EventTypeRunStarted {
		return errors.New("begin run attempt event must be run.started")
	}
	if !numericPayloadEquals(command.Event.Payload["attempt"], command.Attempt) {
		return errors.New("begin run attempt event must contain the attempt number")
	}
	return nil
}

// AppendRunProgressCommand 在 Run 执行过程中追加进度事件。
//
// 与终态提交分开：进度只描述执行到哪一步，不改变 Run 状态，也不产生消息；
// 权威结果仍由 CompleteRunCommand 原子提交。
type AppendRunProgressCommand struct {
	RunID  string
	Events []EventDraft
}

// Validate 限制可在运行期追加的事件类型，避免绕过终态提交写入结果性事件。
func (command AppendRunProgressCommand) Validate() error {
	if err := validateID("agent run ID", command.RunID); err != nil {
		return err
	}
	if len(command.Events) == 0 {
		return errors.New("agent run progress events must not be empty")
	}
	for _, event := range command.Events {
		if err := event.Validate(); err != nil {
			return err
		}
		switch event.Type {
		case EventTypeRetrievalCompleted,
			EventTypeAnswerabilityDecided,
			EventTypeMessageDelta:
		default:
			return errors.New("event type is not allowed as run progress")
		}
	}
	return nil
}

// CompleteRunCommand 是成功结果、Assistant Message 和事件的原子提交。
type CompleteRunCommand struct {
	RunID       string
	Message     Message
	Result      map[string]any
	Events      []EventDraft
	Steps       []RunStepDraft
	CompletedAt time.Time
}

// Validate 校验成功提交的关联关系和事件顺序。
func (command CompleteRunCommand) Validate() error {
	if err := validateID("agent run ID", command.RunID); err != nil {
		return err
	}
	if err := command.Message.Validate(); err != nil {
		return fmt.Errorf("invalid assistant message: %w", err)
	}
	if command.Message.Role != MessageRoleAssistant ||
		command.Message.AgentRunID != command.RunID {
		return errors.New("completion message must belong to the agent run")
	}
	if command.Result == nil {
		return errors.New("agent run result must be a JSON object")
	}
	encodedResult, err := json.Marshal(command.Result)
	if err != nil {
		return errors.New("agent run result must be valid JSON")
	}
	if len(encodedResult) > 256<<10 {
		return errors.New("agent run result exceeds 256 KiB")
	}
	// 检索、决策与回答增量在运行期即已发出，终态提交只补引用并收尾。
	if len(command.Events) == 0 {
		return errors.New("agent run completion events are incomplete")
	}
	for _, event := range command.Events {
		if err := event.Validate(); err != nil {
			return err
		}
	}
	for _, step := range command.Steps {
		if err := step.Validate(); err != nil {
			return err
		}
	}
	if command.Events[len(command.Events)-1].Type != EventTypeRunCompleted {
		return errors.New("agent run completion event order is invalid")
	}
	for _, event := range command.Events[:len(command.Events)-1] {
		if event.Type != EventTypeMessageCitation {
			return errors.New("only citation events may precede run.completed")
		}
	}
	if command.CompletedAt.IsZero() {
		return errors.New("agent run completed time is required")
	}
	return nil
}

// RecordRunFailureCommand 记录可重试尝试或终态失败。
type RecordRunFailureCommand struct {
	RunID      string
	Attempt    int
	ErrorCode  string
	Terminal   bool
	Event      EventDraft
	OccurredAt time.Time
}

// Validate 校验稳定错误码和 retry/final 事件语义。
func (command RecordRunFailureCommand) Validate() error {
	if err := validateID("agent run ID", command.RunID); err != nil {
		return err
	}
	if command.Attempt <= 0 {
		return errors.New("agent run attempt must be greater than zero")
	}
	if !validErrorCode(command.ErrorCode) {
		return errors.New("agent run error code is invalid")
	}
	if err := command.Event.Validate(); err != nil {
		return err
	}
	expectedType := EventTypeRunStatus
	if command.Terminal {
		expectedType = EventTypeRunFailed
	}
	if command.Event.Type != expectedType {
		return errors.New("agent run failure event type does not match terminal state")
	}
	if command.OccurredAt.IsZero() {
		return errors.New("agent run failure time is required")
	}
	return nil
}

func numericPayloadEquals(value any, expected int) bool {
	switch number := value.(type) {
	case int:
		return number == expected
	case int32:
		return int(number) == expected
	case int64:
		return int(number) == expected
	case float64:
		return number == float64(expected)
	default:
		return false
	}
}

func validErrorCode(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 100 {
		return false
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' {
			return false
		}
	}
	return true
}

func validateID(name string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxIDLength {
		return fmt.Errorf("%s must be 1-%d characters", name, maxIDLength)
	}
	return nil
}
