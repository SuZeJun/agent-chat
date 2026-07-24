package chatpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domain "agent-chat/internal/domain/chat"

	"github.com/jackc/pgx/v5"
)

// LoadRunEvents 在客户授权范围内按 sequence 增量读取 Run 事件。
func (repository *Repository) LoadRunEvents(
	ctx context.Context,
	customerID string,
	runID string,
	afterSequence int,
	limit int,
) (domain.RunEventPage, error) {
	customerID = strings.TrimSpace(customerID)
	runID = strings.TrimSpace(runID)
	if customerID == "" || len(customerID) > 64 ||
		runID == "" || len(runID) > 64 ||
		afterSequence < 0 ||
		limit <= 0 || limit > 100 {
		return domain.RunEventPage{}, errors.New("load run events: invalid request")
	}

	transaction, err := repository.database.BeginTx(
		ctx,
		pgx.TxOptions{AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return domain.RunEventPage{}, mapDatabaseError("load run events", err)
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	page := domain.RunEventPage{RunID: runID}
	err = transaction.QueryRow(ctx, `
		SELECT run.status
		FROM agent_runs AS run
		JOIN conversations AS conversation
		  ON conversation.id = run.conversation_id
		WHERE run.id = $1
		  AND conversation.customer_id = $2
	`, runID, customerID).Scan(&page.Status)
	if err != nil {
		return domain.RunEventPage{}, mapDatabaseError("load run event scope", err)
	}

	rows, err := transaction.Query(ctx, `
		SELECT
			id,
			run_id,
			sequence,
			event_type,
			payload,
			created_at
		FROM run_events
		WHERE run_id = $1
		  AND sequence > $2
		ORDER BY sequence
		LIMIT $3
	`, runID, afterSequence, limit)
	if err != nil {
		return domain.RunEventPage{}, mapDatabaseError("load run events", err)
	}
	defer rows.Close()
	page.Events = make([]domain.RunEvent, 0)
	for rows.Next() {
		var event domain.RunEvent
		var payload []byte
		if err := rows.Scan(
			&event.ID,
			&event.RunID,
			&event.Sequence,
			&event.Type,
			&payload,
			&event.CreatedAt,
		); err != nil {
			return domain.RunEventPage{}, mapDatabaseError("scan run event", err)
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return domain.RunEventPage{}, errors.New(
				"load run events: invalid persisted payload",
			)
		}
		if err := event.Validate(); err != nil {
			return domain.RunEventPage{}, fmt.Errorf(
				"load run events: invalid persisted event",
			)
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return domain.RunEventPage{}, mapDatabaseError("load run events", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return domain.RunEventPage{}, mapDatabaseError("load run events", err)
	}
	return page, nil
}
