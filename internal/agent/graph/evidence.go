package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"agent-chat/internal/agent/retrieval"

	"github.com/cloudwego/eino/schema"
)

// selectSources 校验排序与来源元数据，并限制进入 Prompt 的证据数量和总字符数。
func selectSources(
	documents []*schema.Document,
	maxDocuments int,
	maxRunes int,
) ([]source, error) {
	sources := make([]source, 0, min(len(documents), maxDocuments))
	remainingRunes := maxRunes
	previousScore := math.Inf(1)
	for index, document := range documents {
		if len(sources) == maxDocuments || remainingRunes == 0 {
			break
		}
		item, err := sourceFromDocument(document)
		if err != nil {
			return nil, err
		}
		if item.evidence.Rank != index+1 {
			return nil, errors.New("knowledge ranks must be contiguous")
		}
		if item.evidence.Score > previousScore {
			return nil, errors.New("knowledge scores must be descending")
		}
		previousScore = item.evidence.Score
		contentRunes := []rune(item.content)
		if len(contentRunes) > remainingRunes {
			item.content = strings.TrimSpace(string(contentRunes[:remainingRunes]))
			contentRunes = []rune(item.content)
		}
		if len(contentRunes) == 0 {
			return nil, errors.New("knowledge content is empty")
		}
		item.evidence.SourceID = fmt.Sprintf("S%d", len(sources)+1)
		sources = append(sources, item)
		remainingRunes -= len(contentRunes)
	}
	return sources, nil
}

// sourceFromDocument 将 Eino Document 转换为不可缺字段的内部证据，拒绝伪造来源。
func sourceFromDocument(document *schema.Document) (source, error) {
	if document == nil {
		return source{}, errors.New("knowledge document is nil")
	}
	if strings.TrimSpace(document.ID) == "" {
		return source{}, errors.New("knowledge chunk ID is missing")
	}
	if !validScore(document.Score()) {
		return source{}, errors.New("knowledge score is invalid")
	}
	content := strings.TrimSpace(document.Content)
	if content == "" {
		return source{}, errors.New("knowledge content is empty")
	}
	documentID, err := requiredMetadataString(document.MetaData, retrieval.MetadataDocumentID)
	if err != nil {
		return source{}, err
	}
	versionID, err := requiredMetadataString(document.MetaData, retrieval.MetadataVersionID)
	if err != nil {
		return source{}, err
	}
	documentType, err := requiredMetadataString(document.MetaData, retrieval.MetadataDocumentType)
	if err != nil {
		return source{}, err
	}
	title, err := requiredMetadataString(document.MetaData, retrieval.MetadataTitle)
	if err != nil {
		return source{}, err
	}
	rankValue, exists := document.MetaData[retrieval.MetadataRank]
	if !exists {
		return source{}, errors.New("knowledge rank is missing")
	}
	rank, err := metadataInt(rankValue)
	if err != nil || rank <= 0 {
		return source{}, errors.New("knowledge rank is invalid")
	}
	return source{
		evidence: Evidence{
			ChunkID:      strings.TrimSpace(document.ID),
			DocumentID:   documentID,
			VersionID:    versionID,
			DocumentType: documentType,
			Title:        title,
			Score:        document.Score(),
			Rank:         rank,
		},
		content: content,
	}, nil
}

func requiredMetadataString(metadata map[string]any, key string) (string, error) {
	value, exists := metadata[key]
	if !exists {
		return "", fmt.Errorf("knowledge metadata %s is missing", key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("knowledge metadata %s is invalid", key)
	}
	return strings.TrimSpace(text), nil
}

func metadataInt(value any) (int, error) {
	switch number := value.(type) {
	case int:
		return number, nil
	case int32:
		return int(number), nil
	case int64:
		return int(number), nil
	case float64:
		if math.Trunc(number) != number {
			return 0, errors.New("metadata number is not an integer")
		}
		return int(number), nil
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err
	default:
		return 0, errors.New("metadata value is not an integer")
	}
}

// evidenceOf 提取可持久化的证据快照，剥离仅用于 Prompt 的原始内容。
func evidenceOf(sources []source) []Evidence {
	evidence := make([]Evidence, len(sources))
	for index := range sources {
		evidence[index] = sources[index].evidence
	}
	return evidence
}
