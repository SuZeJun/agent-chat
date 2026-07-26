package knowledgebase

import (
	"context"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/knowledge"
)

// Repository 定义创建知识库所需的持久化能力。
type Repository interface {
	CreateBase(context.Context, domain.Base) error
}

// IDGenerator 生成知识库稳定 ID。
type IDGenerator interface {
	NewID(prefix string) string
}

// CreateRequest 是管理员创建知识库的输入。
type CreateRequest struct {
	Name        string
	Description string
}

// CreateResult 返回新知识库的 API 可见字段。
type CreateResult struct {
	ID          string
	Name        string
	Description string
	Status      domain.BaseStatus
}

// Failure 是可安全映射到管理员 API 的稳定错误。
type Failure struct {
	Code  string
	cause error
}

// Error 返回稳定错误码。
func (failure *Failure) Error() string {
	return failure.Code
}

// Unwrap 仅用于进程内错误分类。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// Service 创建知识库。
type Service struct {
	repository  Repository
	idGenerator IDGenerator
}

// NewService 创建知识库 Application Service。
func NewService(repository Repository, idGenerator IDGenerator) (*Service, error) {
	if repository == nil {
		return nil, errors.New("knowledge base repository is required")
	}
	if idGenerator == nil {
		return nil, errors.New("knowledge base ID generator is required")
	}
	return &Service{repository: repository, idGenerator: idGenerator}, nil
}

// Create 校验管理员输入并创建 active 知识库。
func (service *Service) Create(
	ctx context.Context,
	request CreateRequest,
) (CreateResult, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	base := domain.Base{
		ID:          service.idGenerator.NewID("kb_"),
		Name:        request.Name,
		Description: request.Description,
		Status:      domain.BaseStatusActive,
	}
	if err := base.Validate(); err != nil {
		return CreateResult{}, &Failure{Code: "invalid_knowledge_base", cause: err}
	}
	if err := service.repository.CreateBase(ctx, base); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CreateResult{}, err
		}
		code := "create_knowledge_base_failed"
		if errors.Is(err, domain.ErrConflict) {
			code = "knowledge_base_conflict"
		}
		return CreateResult{}, &Failure{Code: code, cause: err}
	}
	return CreateResult{
		ID:          base.ID,
		Name:        base.Name,
		Description: base.Description,
		Status:      base.Status,
	}, nil
}
