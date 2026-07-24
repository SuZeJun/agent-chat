package graph

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	einoretriever "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultAnswerableScoreThreshold    = 0.82
	defaultClarificationScoreThreshold = 0.65
	defaultMaxContextDocuments         = 5
	defaultMaxContextRunes             = 8000
)

// Decision 表示 Answerability Gate 的确定性路由结果。
type Decision string

const (
	// DecisionAnswerable 表示当前知识足以支持受约束回答。
	DecisionAnswerable Decision = "answerable"
	// DecisionNeedsClarification 表示存在相关知识，但支持度不足，需要用户补充信息。
	DecisionNeedsClarification Decision = "needs_clarification"
	// DecisionUnanswerable 表示当前知识不足，系统必须拒绝生成确定性业务结论。
	DecisionUnanswerable Decision = "unanswerable"
)

// NextAction 表示客户端可以为非回答分支提供的下一步操作。
type NextAction string

const (
	// NextActionProvideDetails 提示客户端展示补充信息入口。
	NextActionProvideDetails NextAction = "provide_details"
	// NextActionRequestHumanSupport 提示客户端展示转人工入口。
	NextActionRequestHumanSupport NextAction = "request_human_support"
)

// Input 是 RAG Graph 的最小输入。
type Input struct {
	Query string `json:"query"`
}

// Evidence 是 Answerability 决策实际检查的知识证据。
type Evidence struct {
	SourceID     string  `json:"sourceId"`
	ChunkID      string  `json:"chunkId"`
	DocumentID   string  `json:"documentId"`
	VersionID    string  `json:"versionId"`
	DocumentType string  `json:"documentType"`
	Title        string  `json:"title"`
	Score        float64 `json:"score"`
	Rank         int     `json:"rank"`
}

// Assessment 描述 Answerability 决策、稳定原因、支持置信度和证据。
//
// Confidence 表示最强证据的相似度，归一到 0-1；它不是模型自报的分类置信度。
type Assessment struct {
	Decision   Decision   `json:"decision"`
	Reason     string     `json:"reason"`
	Confidence float64    `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
}

// Citation 是经过服务端校验、且确实被回答中的来源标记引用的知识来源。
type Citation struct {
	Evidence
	Excerpt string `json:"excerpt"`
}

// TraceStep 是通过 Eino Callback 收集的节点或模型调用元数据。
type TraceStep struct {
	Name             string    `json:"name"`
	Component        string    `json:"component"`
	ComponentType    string    `json:"componentType"`
	Status           string    `json:"status"`
	StartedAt        time.Time `json:"startedAt"`
	CompletedAt      time.Time `json:"completedAt"`
	DurationMillis   int64     `json:"durationMillis"`
	PromptTokens     int       `json:"promptTokens,omitempty"`
	CompletionTokens int       `json:"completionTokens,omitempty"`
}

// Output 是 Graph 的结构化结果，供后续 SSE、持久化 Trace 和 UI 直接映射。
type Output struct {
	Answer     string      `json:"answer"`
	Assessment Assessment  `json:"assessment"`
	Citations  []Citation  `json:"citations"`
	NextAction NextAction  `json:"nextAction,omitempty"`
	NodePath   []string    `json:"nodePath"`
	Trace      []TraceStep `json:"trace"`
}

// Config 控制 Answerability 阈值和进入模型 Prompt 的上下文上限。
type Config struct {
	AnswerableScoreThreshold    float64
	ClarificationScoreThreshold float64
	MaxContextDocuments         int
	MaxContextRunes             int
}

// DefaultConfig 返回首版 RAG Graph 的保守默认配置。
func DefaultConfig() Config {
	return Config{
		AnswerableScoreThreshold:    defaultAnswerableScoreThreshold,
		ClarificationScoreThreshold: defaultClarificationScoreThreshold,
		MaxContextDocuments:         defaultMaxContextDocuments,
		MaxContextRunes:             defaultMaxContextRunes,
	}
}

func (config Config) validate() error {
	if !validThreshold(config.AnswerableScoreThreshold) ||
		!validThreshold(config.ClarificationScoreThreshold) {
		return errors.New("answerability thresholds must be finite values between 0 and 1")
	}
	if config.ClarificationScoreThreshold >= config.AnswerableScoreThreshold {
		return errors.New("clarification threshold must be lower than answerable threshold")
	}
	if config.MaxContextDocuments <= 0 || config.MaxContextDocuments > 20 {
		return errors.New("max context documents must be between 1 and 20")
	}
	if config.MaxContextRunes <= 0 || config.MaxContextRunes > 100_000 {
		return errors.New("max context runes must be between 1 and 100000")
	}
	return nil
}

func validScore(score float64) bool {
	return score >= -1 && score <= 1 && !math.IsNaN(score) && !math.IsInf(score, 0)
}

func validThreshold(score float64) bool {
	return score >= 0 && score <= 1 && !math.IsNaN(score) && !math.IsInf(score, 0)
}

// ChatModel 定义 Graph 生成回答所需的最小 Eino ChatModel 能力。
type ChatModel interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

// Failure 是跨 Agent 边界返回的稳定错误，不暴露问题、知识内容或供应商响应。
type Failure struct {
	Code         string
	RetryAllowed bool
	cause        error
}

// Error 返回可安全记录和映射到 API 错误码的稳定字符串。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅供进程内错误判断使用，不应直接记录底层 cause。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// CanRetry 表示调用方是否可以按持久化 Job 策略重试。
func (failure *Failure) CanRetry() bool {
	return failure.RetryAllowed
}

func newFailure(code string, retryAllowed bool, cause error) error {
	return &Failure{
		Code:         code,
		RetryAllowed: retryAllowed,
		cause:        cause,
	}
}

func retryAllowed(err error) bool {
	var retryability interface{ CanRetry() bool }
	return errors.As(err, &retryability) && retryability.CanRetry()
}

type dependencies struct {
	retriever einoretriever.Retriever
	chatModel ChatModel
	config    Config
}

type runState struct {
	query      string
	sources    []source
	assessment Assessment
	answer     string
	citations  []Citation
	nextAction NextAction
	nodePath   []string
}

type source struct {
	evidence Evidence
	content  string
}

func (state runState) output() Output {
	citations := append([]Citation(nil), state.citations...)
	if citations == nil {
		citations = []Citation{}
	}
	return Output{
		Answer:     state.answer,
		Assessment: state.assessment,
		Citations:  citations,
		NextAction: state.nextAction,
		NodePath:   append([]string(nil), state.nodePath...),
		Trace:      []TraceStep{},
	}
}

func normalizeQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	if len([]rune(query)) > 8000 {
		return "", errors.New("query exceeds 8000 characters")
	}
	return query, nil
}
