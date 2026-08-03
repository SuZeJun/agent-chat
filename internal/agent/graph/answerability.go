package graph

import "math"

const (
	reasonKnowledgeSupportSufficient   = "knowledge_support_sufficient"
	reasonKnowledgeSupportAmbiguous    = "knowledge_support_ambiguous"
	reasonKnowledgeSupportInsufficient = "knowledge_support_insufficient"
	// 工具路径的判定原因：依据来自账户数据而非知识库检索。
	reasonToolResultSufficient = "tool_result_sufficient"
	reasonToolExecutionFailed  = "tool_execution_failed"
	reasonTicketDraftPrepared  = "ticket_draft_prepared"
	reasonNoRelevantKnowledge  = "no_relevant_knowledge"
)

// assessAnswerability 只依据服务端检索分数做确定性判断，不接受模型自报置信度。
func assessAnswerability(sources []source, config Config) Assessment {
	evidence := evidenceOf(sources)
	if len(sources) == 0 {
		return Assessment{
			Decision: DecisionUnanswerable,
			Reason:   reasonNoRelevantKnowledge,
			Evidence: evidence,
		}
	}

	confidence := math.Max(0, math.Min(1, sources[0].evidence.Score))
	switch {
	case sources[0].evidence.Score >= config.AnswerableScoreThreshold:
		return Assessment{
			Decision:   DecisionAnswerable,
			Reason:     reasonKnowledgeSupportSufficient,
			Confidence: confidence,
			Evidence:   evidence,
		}
	case sources[0].evidence.Score >= config.ClarificationScoreThreshold:
		return Assessment{
			Decision:   DecisionNeedsClarification,
			Reason:     reasonKnowledgeSupportAmbiguous,
			Confidence: confidence,
			Evidence:   evidence,
		}
	default:
		return Assessment{
			Decision:   DecisionUnanswerable,
			Reason:     reasonKnowledgeSupportInsufficient,
			Confidence: confidence,
			Evidence:   evidence,
		}
	}
}
