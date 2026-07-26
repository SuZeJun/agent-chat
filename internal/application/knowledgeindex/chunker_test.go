package knowledgeindex

import (
	_ "embed"
	"encoding/json"
	"slices"
	"testing"
	"unicode/utf8"

	domain "agent-chat/internal/domain/knowledge"
)

//go:embed testdata/chunking_cases.json
var chunkingCasesJSON []byte

func TestDeterministicChunkerEvalCases(t *testing.T) {
	var cases []struct {
		Name           string   `json:"name"`
		DocumentType   string   `json:"documentType"`
		Title          string   `json:"title"`
		Content        string   `json:"content"`
		ExpectedChunks []string `json:"expectedChunks"`
	}
	if err := json.Unmarshal(chunkingCasesJSON, &cases); err != nil {
		t.Fatalf("decode chunking eval cases: %v", err)
	}

	chunker := NewDeterministicChunker()
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			source := testIndexSource(domain.DocumentType(test.DocumentType), test.Title, test.Content)
			drafts, err := chunker.Split(source)
			if err != nil {
				t.Fatalf("Split returned error: %v", err)
			}
			actual := make([]string, len(drafts))
			for index, draft := range drafts {
				actual[index] = draft.Content
				if draft.TokenCount != utf8.RuneCountInString(draft.Content) {
					t.Fatalf("unexpected token estimate: %d", draft.TokenCount)
				}
				if draft.Metadata["document_id"] != source.Document.ID ||
					draft.Metadata["version_id"] != source.Version.ID ||
					draft.Metadata["chunk_position"] != index {
					t.Fatalf("unexpected chunk metadata: %#v", draft.Metadata)
				}
			}
			if !slices.Equal(actual, test.ExpectedChunks) {
				t.Fatalf("unexpected chunks:\nactual: %#v\nexpected: %#v", actual, test.ExpectedChunks)
			}
		})
	}
}

func TestDeterministicChunkerUsesBoundedOverlapForLongBlock(t *testing.T) {
	chunker := &DeterministicChunker{maxRunes: 10, overlap: 2}
	source := testIndexSource(domain.DocumentTypeMarkdown, "长文档", "1234567890ABCD")

	drafts, err := chunker.Split(source)
	if err != nil {
		t.Fatalf("Split returned error: %v", err)
	}
	actual := []string{drafts[0].Content, drafts[1].Content}
	expected := []string{"1234567890", "90ABCD"}
	if !slices.Equal(actual, expected) {
		t.Fatalf("unexpected overlapping chunks: %#v", actual)
	}
}

func testIndexSource(documentType domain.DocumentType, title string, content string) domain.IndexSource {
	identity := domain.EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	return domain.IndexSource{
		Document: domain.Document{
			ID:              "document-1",
			KnowledgeBaseID: "base-1",
			Type:            documentType,
			Title:           title,
			Metadata:        map[string]any{"locale": "zh-CN"},
		},
		Version: domain.Version{
			ID:                "version-1",
			DocumentID:        "document-1",
			Number:            1,
			Content:           content,
			ContentSHA256:     domain.ContentChecksum(content),
			EmbeddingIdentity: identity,
		},
		Status: domain.IndexStatusPending,
	}
}
