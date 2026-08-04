package knowledgebase

import (
	"context"
	"errors"
	"testing"

	domain "agent-chat/internal/domain/knowledge"
)

type fakeRepository struct {
	base  domain.Base
	bases []domain.Base
	err   error
}

func (repository *fakeRepository) CreateBase(_ context.Context, base domain.Base) error {
	repository.base = base
	return repository.err
}

func (repository *fakeRepository) ListBases(context.Context) ([]domain.Base, error) {
	return repository.bases, repository.err
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

func TestListKnowledgeBases(t *testing.T) {
	repository := &fakeRepository{bases: []domain.Base{
		{ID: "kb_a", Name: "产品知识", Status: domain.BaseStatusActive},
		{ID: "kb_b", Name: "历史知识", Status: domain.BaseStatusDisabled},
	}}
	service, err := NewService(repository, fixedGenerator{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].ID != "kb_a" ||
		items[1].Status != domain.BaseStatusDisabled {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestListKnowledgeBasesMapsRepositoryError(t *testing.T) {
	service, err := NewService(&fakeRepository{err: errors.New("database unavailable")}, fixedGenerator{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.List(context.Background())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "list_knowledge_bases_failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}
