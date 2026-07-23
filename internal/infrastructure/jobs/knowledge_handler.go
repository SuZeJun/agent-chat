package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"agent-chat/internal/application/knowledgeindex"
	domain "agent-chat/internal/domain/knowledge"
)

// KnowledgeIndexer 定义 knowledge.index Job 适配器调用的 Application 用例。
type KnowledgeIndexer interface {
	IndexVersion(context.Context, string) error
}

// KnowledgeIndexHandler 将持久化 Job Payload 转换为知识索引用例输入。
type KnowledgeIndexHandler struct {
	indexer KnowledgeIndexer
}

// NewKnowledgeIndexHandler 创建 knowledge.index Job Handler。
func NewKnowledgeIndexHandler(indexer KnowledgeIndexer) (*KnowledgeIndexHandler, error) {
	if indexer == nil {
		return nil, errors.New("knowledge indexer is required")
	}
	return &KnowledgeIndexHandler{indexer: indexer}, nil
}

// Handle 校验稳定任务类型、Payload 和幂等键后执行知识索引用例。
func (handler *KnowledgeIndexHandler) Handle(ctx context.Context, job Job) error {
	if job.Type != domain.IndexJobType {
		return Permanent("invalid_job_type", nil)
	}
	payload, err := decodeKnowledgeIndexPayload(job.Payload)
	if err != nil {
		return Permanent("invalid_job_payload", err)
	}
	if job.IdempotencyKey != "" && job.IdempotencyKey != payload.VersionID {
		return Permanent("job_idempotency_mismatch", nil)
	}

	err = handler.indexer.IndexVersion(ctx, payload.VersionID)
	if err == nil {
		return nil
	}
	var failure *knowledgeindex.Failure
	if errors.As(err, &failure) {
		if failure.RetryAllowed {
			return Retryable(failure.Code, err)
		}
		return Permanent(failure.Code, err)
	}
	return Retryable("knowledge_index_failed", err)
}

type knowledgeIndexPayload struct {
	VersionID string `json:"version_id"`
}

func decodeKnowledgeIndexPayload(rawPayload json.RawMessage) (knowledgeIndexPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(rawPayload))
	decoder.DisallowUnknownFields()
	var payload knowledgeIndexPayload
	if err := decoder.Decode(&payload); err != nil {
		return knowledgeIndexPayload{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return knowledgeIndexPayload{}, err
	}
	payload.VersionID = strings.TrimSpace(payload.VersionID)
	if payload.VersionID == "" || len(payload.VersionID) > 64 {
		return knowledgeIndexPayload{}, errors.New("version ID must be 1-64 characters")
	}
	return payload, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("payload must contain one JSON value")
	}
	return err
}
