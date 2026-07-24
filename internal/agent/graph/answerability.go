package graph

import "math"

const (
	reasonKnowledgeSupportSufficient   = "knowledge_support_sufficient"
	reasonKnowledgeSupportAmbiguous    = "knowledge_support_ambiguous"
	reasonKnowledgeSupportInsufficient = "knowledge_support_insufficient"
	reasonNoRelevantKnowledge          = "no_relevant_knowledge"
)

func assessAnswerability(sources []source, config Config) Assessment {
	evidence := make([]Evidence, len(sources))
	for index := range sources {
		evidence[index] = sources[index].evidence
	}
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
