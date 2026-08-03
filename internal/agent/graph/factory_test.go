package graph

import (
	"context"
	"testing"

	agenttool "agent-chat/internal/agent/tool"
	"agent-chat/internal/domain/crm"
)

type factorySubscriptionReader struct{}

func (factorySubscriptionReader) LoadSubscription(
	context.Context,
	string,
) (crm.Subscription, error) {
	return crm.Subscription{}, nil
}

func TestProductionToolRegistryIncludesDraftTicket(t *testing.T) {
	t.Parallel()

	registry, err := newToolRegistry(factorySubscriptionReader{}, "customer-1")
	if err != nil {
		t.Fatalf("new production tool registry: %v", err)
	}
	infos, err := registry.Infos(context.Background())
	if err != nil {
		t.Fatalf("read production tool registry: %v", err)
	}

	want := []string{agenttool.SubscriptionToolName, agenttool.DraftTicketToolName}
	if len(infos) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(infos), len(want))
	}
	for index, name := range want {
		if infos[index].Name != name {
			t.Fatalf("tool %d = %q, want %q", index, infos[index].Name, name)
		}
	}
}
