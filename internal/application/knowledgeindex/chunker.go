package knowledgeindex

import (
	"errors"
	"strings"
	"unicode/utf8"

	domain "agent-chat/internal/domain/knowledge"
)

const (
	defaultMaxChunkRunes = 1200
	defaultOverlapRunes  = 120
)

// ChunkDraft 是尚未生成 embedding 的确定性切片结果。
type ChunkDraft struct {
	Content    string
	TokenCount int
	Metadata   map[string]any
}

// Chunker 将不可变知识版本转换为有序切片草稿。
type Chunker interface {
	Split(domain.IndexSource) ([]ChunkDraft, error)
}

// DeterministicChunker 使用固定字符窗口生成跨平台一致的 FAQ/Markdown 切片。
type DeterministicChunker struct {
	maxRunes int
	overlap  int
}

// NewDeterministicChunker 创建固定 1200 rune 上限和 120 rune 重叠的切片器。
func NewDeterministicChunker() *DeterministicChunker {
	return &DeterministicChunker{
		maxRunes: defaultMaxChunkRunes,
		overlap:  defaultOverlapRunes,
	}
}

// Split 按文档类型生成切片；FAQ 保持问答原子性，Markdown 优先按结构块打包。
func (chunker *DeterministicChunker) Split(source domain.IndexSource) ([]ChunkDraft, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}

	var contents []string
	switch source.Document.Type {
	case domain.DocumentTypeFAQ:
		content := "问题：" + strings.TrimSpace(source.Document.Title) +
			"\n答案：" + strings.TrimSpace(normalizeNewlines(source.Version.Content))
		contents = splitLongText(content, chunker.maxRunes, chunker.overlap)
	case domain.DocumentTypeMarkdown:
		contents = packMarkdownBlocks(
			splitMarkdownBlocks(source.Version.Content),
			chunker.maxRunes,
			chunker.overlap,
		)
	default:
		return nil, errors.New("unsupported document type")
	}
	if len(contents) == 0 {
		return nil, errors.New("document produced no chunks")
	}

	drafts := make([]ChunkDraft, len(contents))
	for position, content := range contents {
		metadata := cloneMetadata(source.Document.Metadata)
		metadata["knowledge_base_id"] = source.Document.KnowledgeBaseID
		metadata["document_id"] = source.Document.ID
		metadata["version_id"] = source.Version.ID
		metadata["document_type"] = string(source.Document.Type)
		metadata["title"] = source.Document.Title
		metadata["chunk_position"] = position
		drafts[position] = ChunkDraft{
			Content:    content,
			TokenCount: utf8.RuneCountInString(content),
			Metadata:   metadata,
		}
	}
	return drafts, nil
}

func splitMarkdownBlocks(content string) []string {
	lines := strings.Split(normalizeNewlines(content), "\n")
	blocks := make([]string, 0)
	current := make([]string, 0)
	flush := func() {
		block := strings.TrimSpace(strings.Join(current, "\n"))
		if block != "" {
			blocks = append(blocks, block)
		}
		current = current[:0]
	}
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return blocks
}

func packMarkdownBlocks(blocks []string, maxRunes int, overlap int) []string {
	chunks := make([]string, 0)
	current := ""
	flush := func() {
		if current != "" {
			chunks = append(chunks, current)
			current = ""
		}
	}

	for _, block := range blocks {
		if utf8.RuneCountInString(block) > maxRunes {
			flush()
			chunks = append(chunks, splitLongText(block, maxRunes, overlap)...)
			continue
		}
		candidate := block
		if current != "" {
			candidate = current + "\n\n" + block
		}
		if utf8.RuneCountInString(candidate) <= maxRunes {
			current = candidate
			continue
		}
		flush()
		current = block
	}
	flush()
	return chunks
}

func splitLongText(content string, maxRunes int, overlap int) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return []string{content}
	}

	chunks := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	step := maxRunes - overlap
	for start := 0; start < len(runes); start += step {
		end := min(start+maxRunes, len(runes))
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func normalizeNewlines(content string) string {
	return strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
}

func cloneMetadata(metadata map[string]any) map[string]any {
	cloned := make(map[string]any, len(metadata)+6)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
