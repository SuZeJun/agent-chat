package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/chat"
)

// LoadRunTrace 读取 Run 关联 ID、最终结果和 Eino Callback 节点。
func (repository *Repository) LoadRunTrace(
	ctx context.Context,
	runID string,
) (domain.RunTraceSnapshot, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || len(runID) > 64 {
		return domain.RunTraceSnapshot{}, errors.New("load run trace: invalid run ID")
	}
	var trace domain.RunTraceSnapshot
	var rawResult []byte
	err := repository.database.QueryRow(ctx, `
		SELECT
			run.id,
			run.request_id,
			run.conversation_id,
			source.content,
			run.status,
			run.result,
			run.error_code,
			run.created_at,
			run.started_at,
			run.completed_at
		FROM agent_runs AS run
		JOIN messages AS source
		  ON source.id = run.source_message_id
		WHERE run.id = $1
	`, runID).Scan(
		&trace.RunID,
		&trace.RequestID,
		&trace.ConversationID,
		&trace.Question,
		&trace.Status,
		&rawResult,
		&trace.ErrorCode,
		&trace.CreatedAt,
		&trace.StartedAt,
		&trace.CompletedAt,
	)
	if err != nil {
		return domain.RunTraceSnapshot{}, mapDatabaseError("load run trace", err)
	}
	if err := json.Unmarshal(rawResult, &trace.Result); err != nil {
		return domain.RunTraceSnapshot{}, errors.New(
			"load run trace: invalid persisted result",
		)
	}

	rows, err := repository.database.Query(ctx, `
		SELECT
			step_order,
			name,
			component,
			component_type,
			status,
			started_at,
			completed_at,
			duration_ms,
			prompt_tokens,
			completion_tokens
		FROM agent_run_steps
		WHERE run_id = $1
		ORDER BY step_order
	`, runID)
	if err != nil {
		return domain.RunTraceSnapshot{}, mapDatabaseError("load run trace steps", err)
	}
	defer rows.Close()
	trace.Steps = make([]domain.RunStep, 0)
	for rows.Next() {
		var step domain.RunStep
		if err := rows.Scan(
			&step.Order,
			&step.Name,
			&step.Component,
			&step.ComponentType,
			&step.Status,
			&step.StartedAt,
			&step.CompletedAt,
			&step.DurationMillis,
			&step.PromptTokens,
			&step.CompletionTokens,
		); err != nil {
			return domain.RunTraceSnapshot{}, mapDatabaseError("scan run trace step", err)
		}
		if step.Order != len(trace.Steps)+1 {
			return domain.RunTraceSnapshot{}, errors.New(
				"load run trace: step order is inconsistent",
			)
		}
		if err := step.Validate(); err != nil {
			return domain.RunTraceSnapshot{}, errors.New(
				"load run trace: invalid persisted step",
			)
		}
		trace.Steps = append(trace.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return domain.RunTraceSnapshot{}, mapDatabaseError("load run trace steps", err)
	}
	return trace, nil
}
