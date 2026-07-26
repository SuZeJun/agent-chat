package knowledgebase

import (
	"context"
	"errors"
	"testing"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	base domain.Base
	err  error
}

func (repository *fakeRepository) CreateBase(_ context.Context, base domain.Base) error {
	repository.base = base
	return repository.err
}

type fixedGenerator struct{}

func (fixedGenerator) NewID(string) string {
	return "kb_test"
}

func TestCreateKnowledgeBase(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(repository, fixedGenerator{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	result, err := service.Create(context.Background(), CreateRequest{
		Name:        "  产品帮助中心 ",
		Description: " SaaS FAQ ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.ID != "kb_test" ||
		result.Name != "产品帮助中心" ||
		result.Description != "SaaS FAQ" ||
		result.Status != domain.BaseStatusActive ||
		repository.base.ID != result.ID {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCreateKnowledgeBaseMapsErrors(t *testing.T) {
	tests := []struct {
		name            string
		repositoryError error
		expectedCode    string
		request         CreateRequest
	}{
		{
			name:         "invalid",
			expectedCode: "invalid_knowledge_base",
			request:      CreateRequest{},
		},
		{
			name:            "conflict",
			repositoryError: domain.ErrConflict,
			expectedCode:    "knowledge_base_conflict",
			request:         CreateRequest{Name: "知识库"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(
				&fakeRepository{err: test.repositoryError},
				fixedGenerator{},
			)
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			_, err = service.Create(context.Background(), test.request)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != test.expectedCode {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
