package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type answerabilityCase struct {
	Name     string    `json:"name"`
	Query    string    `json:"query"`
	Scores   []float64 `json:"scores"`
	Decision Decision  `json:"decision"`
	Reason   string    `json:"reason"`
}

func TestAnswerabilityEvalCases(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "answerability_cases.json"))
	if err != nil {
		t.Fatalf("read answerability cases: %v", err)
	}
	var cases []answerabilityCase
	if err := json.Unmarshal(content, &cases); err != nil {
		t.Fatalf("decode answerability cases: %v", err)
	}
	// 与 Eval 的安全门槛保持一致的下限；语料扩充后用例只增不减。
	if len(cases) < 10 {
		t.Fatalf("expected at least 10 answerability cases, got %d", len(cases))
	}

	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			sources := make([]source, len(test.Scores))
			for index, score := range test.Scores {
				sources[index] = source{
					evidence: Evidence{
						SourceID: "S1",
						Score:    score,
					},
				}
			}
			assessment := assessAnswerability(sources, DefaultConfig())
			if assessment.Decision != test.Decision || assessment.Reason != test.Reason {
				t.Fatalf(
					"unexpected assessment for %q: %#v",
					test.Query,
					assessment,
				)
			}
			if len(assessment.Evidence) != len(test.Scores) {
				t.Fatalf("unexpected evidence count: %#v", assessment.Evidence)
			}
		})
	}
}

func TestConfigRejectsUnsafeThresholdsAndContextLimits(t *testing.T) {
	tests := []Config{
		{
			AnswerableScoreThreshold:    0.7,
			ClarificationScoreThreshold: 0.7,
			MaxContextDocuments:         5,
			MaxContextRunes:             8000,
		},
		{
			AnswerableScoreThreshold:    1.1,
			ClarificationScoreThreshold: 0.6,
			MaxContextDocuments:         5,
			MaxContextRunes:             8000,
		},
		{
			AnswerableScoreThreshold:    0.8,
			ClarificationScoreThreshold: -0.1,
			MaxContextDocuments:         5,
			MaxContextRunes:             8000,
		},
		{
			AnswerableScoreThreshold:    0.8,
			ClarificationScoreThreshold: 0.6,
			MaxContextDocuments:         0,
			MaxContextRunes:             8000,
		},
		{
			AnswerableScoreThreshold:    0.8,
			ClarificationScoreThreshold: 0.6,
			MaxContextDocuments:         5,
			MaxContextRunes:             100_001,
		},
	}

	for index, config := range tests {
		if err := config.validate(); err == nil {
			t.Fatalf("case %d expected config error", index)
		}
	}
}
