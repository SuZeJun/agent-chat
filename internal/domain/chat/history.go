package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MessageHistoryQuery 是客户作用域内的会话历史分页查询。
//
// BeforeMessageID 是不透明游标；Repository 必须先确认它属于同一客户会话，
// 不能把客户端提供的 ID 直接用于跨会话定位。
type MessageHistoryQuery struct {
	CustomerID      string
	ConversationID  string
	BeforeMessageID string
	Limit           int
}

// Validate 校验历史查询的作用域和分页上限。
func (query MessageHistoryQuery) Validate() error {
	if err := validateID("customer ID", query.CustomerID); err != nil {
		return err
	}
	if err := validateID("conversation ID", query.ConversationID); err != nil {
		return err
	}
	if strings.TrimSpace(query.BeforeMessageID) != "" {
		if err := validateID("history cursor", query.BeforeMessageID); err != nil {
			return err
		}
	}
	if query.Limit <= 0 || query.Limit > 100 {
		return errors.New("history page limit must be between 1 and 100")
	}
	return nil
}

// MessageHistoryItem 是一条消息及其关联 Run 的可恢复快照。
//
// 客户消息携带其触发 Run 的状态，页面刷新后可以继续订阅 pending/running Run；
// Assistant 消息还携带最终 Result，以恢复引用和 Answerability 呈现。
type MessageHistoryItem struct {
	Message      Message
	RunID        string
	RunStatus    RunStatus
	RunResult    map[string]any
	RunErrorCode string
}

// Validate 校验消息与 Run 快照的关联关系和 JSON 边界。
func (item MessageHistoryItem) Validate() error {
	if err := item.Message.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(item.RunID) == "" {
		if item.Message.Role == MessageRoleCustomer || item.Message.Role == MessageRoleAssistant {
			return errors.New("customer and assistant history must reference an agent run")
		}
		if item.RunStatus != "" || item.RunResult != nil || item.RunErrorCode != "" {
			return errors.New("history without a run contains run fields")
		}
		return nil
	}
	if err := validateID("history agent run ID", item.RunID); err != nil {
		return err
	}
	if !item.RunStatus.Valid() {
		return fmt.Errorf("invalid history run status %q", item.RunStatus)
	}
	if item.Message.Role == MessageRoleAssistant &&
		item.Message.AgentRunID != item.RunID {
		return errors.New("assistant history does not match its agent run")
	}
	if item.RunResult != nil {
		encoded, err := json.Marshal(item.RunResult)
		if err != nil || len(encoded) > 256<<10 {
			return errors.New("history run result is invalid")
		}
	}
	if item.RunStatus == RunStatusFailed {
		if strings.TrimSpace(item.RunErrorCode) == "" {
			return errors.New("failed history run requires an error code")
		}
	} else if item.RunErrorCode != "" {
		return errors.New("non-failed history run must not contain an error code")
	}
	return nil
}

// MessageHistoryPage 返回按时间升序排列的一页消息和更早页游标。
type MessageHistoryPage struct {
	Items               []MessageHistoryItem
	NextBeforeMessageID string
	ConversationStatus  ConversationStatus
}
