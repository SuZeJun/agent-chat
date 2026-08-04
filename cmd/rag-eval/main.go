package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentgraph "agent-chat/internal/agent/graph"
	"agent-chat/internal/agent/retrieval"

	"github.com/cloudwego/eino/components/model"
	einoretriever "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// evalCase 描述一个不依赖真实 Provider 的 RAG 路由评估样本。
type evalCase struct {
	Name       string    `json:"name"`
	Query      string    `json:"query"`
	Scores     []float64 `json:"scores"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	NextAction string    `json:"nextAction,omitempty"`
}

// caseResult 记录单个样本的实际决策、调用约束和失败原因。
type caseResult struct {
	Name             string `json:"name"`
	Passed           bool   `json:"passed"`
	ExpectedDecision string `json:"expectedDecision"`
	ActualDecision   string `json:"actualDecision"`
	ExpectedReason   string `json:"expectedReason"`
	ActualReason     string `json:"actualReason"`
	ExpectedAction   string `json:"expectedAction,omitempty"`
	ActualAction     string `json:"actualAction,omitempty"`
	CitationCount    int    `json:"citationCount"`
	ModelCalls       int    `json:"modelCalls"`
	Error            string `json:"error,omitempty"`
}

// report 汇总全部样本，并作为 JSON 与 Markdown 报告的共同数据源。
type report struct {
	Total   int          `json:"total"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	Results []caseResult `json:"results"`
}

// fixtureRetriever 将评估文件中的固定分数转换为 Eino 检索结果。
type fixtureRetriever struct {
	documents []*schema.Document
}

// Retrieve 返回当前评估样本预设的有序证据，避免离线 Eval 依赖外部服务。
func (retriever *fixtureRetriever) Retrieve(
	_ context.Context,
	_ string,
	_ ...einoretriever.Option,
) ([]*schema.Document, error) {
	return retriever.documents, nil
}

// fixtureModel 记录是否发生模型调用，并返回带固定引用的受约束回答。
type fixtureModel struct {
	calls int
}

// fixtureAnswer 是 answerable 分支的确定性回答。
const fixtureAnswer = "这是由企业知识支持的回答。[S1]"

// Generate 为 answerable 分支提供确定性回答，其他分支不应调用该方法。
func (chatModel *fixtureModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	chatModel.calls++
	return schema.AssistantMessage(fixtureAnswer, nil), nil
}

// Stream 分块返回同一回答，使评估与生产一样走流式路径。
//
// 分块长度刻意小于来源标记，确保 [S1] 跨越增量边界，评估因此也覆盖
// 「引用必须在流结束后解析」这一约束。
func (chatModel *fixtureModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	chatModel.calls++
	runes := []rune(fixtureAnswer)
	reader, writer := schema.Pipe[*schema.Message](len(runes))
	go func() {
		defer writer.Close()
		for start := 0; start < len(runes); start += 3 {
			end := min(start+3, len(runes))
			writer.Send(schema.AssistantMessage(string(runes[start:end]), nil), nil)
		}
	}()
	return reader, nil
}

// main 解析报告路径，并通过退出码向 pytest 和 CI 暴露评估门槛结果。
func main() {
	var casesPath string
	var jsonPath string
	var markdownPath string
	flag.StringVar(&casesPath, "cases", "evals/cases/rag_mvp.json", "评估集路径")
	flag.StringVar(&jsonPath, "json", "evals/reports/latest.json", "JSON 报告路径")
	flag.StringVar(&markdownPath, "markdown", "evals/reports/latest.md", "Markdown 报告路径")
	flag.Parse()

	evaluation, err := run(casesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := writeReports(evaluation, jsonPath, markdownPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf(
		"RAG Eval: total=%d passed=%d failed=%d\n",
		evaluation.Total,
		evaluation.Passed,
		evaluation.Failed,
	)
	if evaluation.Failed > 0 {
		os.Exit(1)
	}
}

// run 加载全部用例并实际执行 Eino Graph，而不是仅比较静态期望值。
func run(casesPath string) (report, error) {
	content, err := os.ReadFile(casesPath)
	if err != nil {
		return report{}, fmt.Errorf("read Eval cases: %w", err)
	}
	var cases []evalCase
	if err := json.Unmarshal(content, &cases); err != nil {
		return report{}, fmt.Errorf("decode Eval cases: %w", err)
	}
	if len(cases) < 10 {
		return report{}, fmt.Errorf("RAG Eval requires at least 10 cases")
	}

	evaluation := report{
		Total:   len(cases),
		Results: make([]caseResult, 0, len(cases)),
	}
	for _, item := range cases {
		result := evaluate(item)
		if result.Passed {
			evaluation.Passed++
		} else {
			evaluation.Failed++
		}
		evaluation.Results = append(evaluation.Results, result)
	}
	return evaluation, nil
}

// evaluate 同时验证决策、引用和模型调用次数，防止非回答分支绕过安全门。
func evaluate(item evalCase) caseResult {
	documents := make([]*schema.Document, len(item.Scores))
	for index, score := range item.Scores {
		document := &schema.Document{
			ID:      fmt.Sprintf("chunk-%d", index+1),
			Content: fmt.Sprintf("问题：%s\n答案：知识答案 %d", item.Query, index+1),
			MetaData: map[string]any{
				retrieval.MetadataDocumentID:   fmt.Sprintf("document-%d", index+1),
				retrieval.MetadataVersionID:    fmt.Sprintf("version-%d", index+1),
				retrieval.MetadataDocumentType: "faq",
				retrieval.MetadataTitle:        fmt.Sprintf("FAQ %d", index+1),
				retrieval.MetadataRank:         index + 1,
			},
		}
		document.WithScore(score)
		documents[index] = document
	}
	chatModel := &fixtureModel{}
	runtime, err := agentgraph.NewRuntime(
		context.Background(),
		&fixtureRetriever{documents: documents},
		chatModel,
		agentgraph.DefaultConfig(),
	)
	result := caseResult{
		Name:             item.Name,
		ExpectedDecision: item.Decision,
		ExpectedReason:   item.Reason,
		ExpectedAction:   item.NextAction,
	}
	if err != nil {
		result.Error = "runtime_build_failed"
		return result
	}
	output, err := runtime.Run(context.Background(), agentgraph.Input{Query: item.Query})
	if err != nil {
		result.Error = "runtime_execution_failed"
		return result
	}
	result.ActualDecision = string(output.Assessment.Decision)
	result.ActualReason = output.Assessment.Reason
	result.ActualAction = string(output.NextAction)
	result.CitationCount = len(output.Citations)
	result.ModelCalls = chatModel.calls

	answerable := item.Decision == string(agentgraph.DecisionAnswerable)
	citationSafe := (!answerable && len(output.Citations) == 0) ||
		(answerable &&
			len(output.Citations) == 1 &&
			output.Citations[0].SourceID == "S1")
	modelSafe := (!answerable && chatModel.calls == 0) ||
		(answerable && chatModel.calls == 1)
	result.Passed = result.ActualDecision == item.Decision &&
		result.ActualReason == item.Reason &&
		(item.NextAction == "" || result.ActualAction == item.NextAction) &&
		citationSafe &&
		modelSafe
	if !result.Passed {
		result.Error = "safety_gate_failed"
	}
	return result
}

// writeReports 从同一评估结果生成机器可读和人工可读报告。
func writeReports(evaluation report, jsonPath string, markdownPath string) error {
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		return fmt.Errorf("create JSON report directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(markdownPath), 0o755); err != nil {
		return fmt.Errorf("create Markdown report directory: %w", err)
	}
	encoded, err := json.MarshalIndent(evaluation, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(jsonPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}

	var markdown strings.Builder
	markdown.WriteString("# RAG MVP Eval\n\n")
	fmt.Fprintf(
		&markdown,
		"- Total: %d\n- Passed: %d\n- Failed: %d\n\n",
		evaluation.Total,
		evaluation.Passed,
		evaluation.Failed,
	)
	markdown.WriteString("| Case | Decision | Result |\n")
	markdown.WriteString("| --- | --- | --- |\n")
	for _, result := range evaluation.Results {
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(
			&markdown,
			"| %s | %s | %s |\n",
			strings.ReplaceAll(result.Name, "|", `\|`),
			result.ActualDecision,
			status,
		)
	}
	if err := os.WriteFile(markdownPath, []byte(markdown.String()), 0o644); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	return nil
}
