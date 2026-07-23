package knowledge

import (
	"math"
	"testing"
)

func TestVersionValidateChecksContentChecksum(t *testing.T) {
	t.Parallel()

	version := Version{
		ID:            "version-1",
		DocumentID:    "document-1",
		Number:        1,
		Content:       "问题与答案",
		ContentSHA256: ContentChecksum("其他内容"),
		EmbeddingIdentity: EmbeddingIdentity{
			Provider:   "zhipu",
			Model:      "embedding-3",
			Dimensions: 1024,
		},
	}
	if err := version.Validate(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestChunkValidateRejectsInvalidEmbedding(t *testing.T) {
	t.Parallel()

	identity := EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	chunk := Chunk{
		ID:         "chunk-1",
		VersionID:  "version-1",
		Content:    "内容",
		TokenCount: 1,
		Embedding:  make([]float64, 1024),
	}
	chunk.Embedding[0] = math.NaN()
	if err := chunk.Validate(identity); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSearchQueryRejectsZeroVector(t *testing.T) {
	t.Parallel()

	query := SearchQuery{
		KnowledgeBaseID: "base-1",
		EmbeddingIdentity: EmbeddingIdentity{
			Provider:   "zhipu",
			Model:      "embedding-3",
			Dimensions: 1024,
		},
		Embedding:         make([]float64, 1024),
		Limit:             5,
		MinimumSimilarity: 0.5,
	}
	if err := query.Validate(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestEmbeddingIdentityRequiresExactMatch(t *testing.T) {
	t.Parallel()

	identity := EmbeddingIdentity{
		Provider:   "zhipu",
		Model:      "embedding-3",
		Dimensions: 1024,
	}
	other := identity
	other.Model = "another-model"
	if identity.Equal(other) {
		t.Fatal("expected identities not to match")
	}
}
