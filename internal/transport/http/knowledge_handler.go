package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"agent-chat/internal/application/knowledgebase"
	"agent-chat/internal/application/knowledgeimport"
	domain "agent-chat/internal/domain/knowledge"

	"github.com/gin-gonic/gin"
)

const maxFAQUploadBytes = 2 << 20

// KnowledgeBaseCreator 定义知识库创建 Handler 依赖的 Application 用例。
type KnowledgeBaseCreator interface {
	Create(context.Context, knowledgebase.CreateRequest) (knowledgebase.CreateResult, error)
}

// FAQImportService 定义 FAQ 导入和状态查询 Handler 依赖的 Application 用例。
type FAQImportService interface {
	ImportFAQs(context.Context, knowledgeimport.ImportRequest) (knowledgeimport.ImportResult, error)
	GetStatus(context.Context, string, string) (domain.FAQImportSnapshot, error)
}

type createKnowledgeBaseRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type knowledgeBaseResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type faqImportResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	TotalRows  int    `json:"totalRows"`
	ReadyRows  int    `json:"readyRows"`
	FailedRows int    `json:"failedRows"`
	Duplicate  bool   `json:"duplicate,omitempty"`
}

type faqImportStatusResponse struct {
	ID         string                  `json:"id"`
	SourceName string                  `json:"sourceName"`
	Status     string                  `json:"status"`
	TotalRows  int                     `json:"totalRows"`
	ReadyRows  int                     `json:"readyRows"`
	FailedRows int                     `json:"failedRows"`
	Items      []faqImportItemResponse `json:"items"`
	CreatedAt  string                  `json:"createdAt"`
}

type faqImportItemResponse struct {
	RowNumber  int    `json:"rowNumber"`
	DocumentID string `json:"documentId"`
	VersionID  string `json:"versionId"`
	Status     string `json:"status"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

func registerKnowledgeRoutes(
	router *gin.Engine,
	baseCreator KnowledgeBaseCreator,
	importService FAQImportService,
) {
	admin := router.Group("/api/v1/admin")
	if baseCreator != nil {
		admin.POST("/knowledge-bases", createKnowledgeBaseHandler(baseCreator))
	}
	if importService != nil {
		admin.POST(
			"/knowledge-bases/:knowledgeBaseId/faq-imports",
			importFAQHandler(importService),
		)
		admin.GET(
			"/knowledge-bases/:knowledgeBaseId/faq-imports/:importId",
			getFAQImportHandler(importService),
		)
	}
}

func createKnowledgeBaseHandler(service KnowledgeBaseCreator) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		var request createKnowledgeBaseRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.Create(ctx.Request.Context(), knowledgebase.CreateRequest{
			Name:        request.Name,
			Description: request.Description,
		})
		if err != nil {
			writeKnowledgeBaseError(ctx, err)
			return
		}
		ctx.JSON(http.StatusCreated, knowledgeBaseResponse{
			ID:          result.ID,
			Name:        result.Name,
			Description: result.Description,
			Status:      string(result.Status),
		})
	}
}

func importFAQHandler(service FAQImportService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		fileHeader, err := ctx.FormFile("file")
		if err != nil || fileHeader.Size <= 0 || fileHeader.Size > maxFAQUploadBytes {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_faq_file", "FAQ CSV file is required")
			return
		}
		file, err := fileHeader.Open()
		if err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_faq_file", "FAQ CSV file cannot be read")
			return
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, maxFAQUploadBytes+1))
		if err != nil || len(content) > maxFAQUploadBytes {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_faq_file", "FAQ CSV file is too large")
			return
		}
		result, err := service.ImportFAQs(
			ctx.Request.Context(),
			knowledgeimport.ImportRequest{
				KnowledgeBaseID: ctx.Param("knowledgeBaseId"),
				SourceName:      fileHeader.Filename,
				Content:         content,
			},
		)
		if err != nil {
			writeFAQImportError(ctx, err)
			return
		}
		status := http.StatusAccepted
		if result.Duplicate {
			status = http.StatusOK
		}
		ctx.JSON(status, faqImportResponse{
			ID:        result.ImportID,
			Status:    string(result.Status),
			TotalRows: result.TotalRows,
			Duplicate: result.Duplicate,
		})
	}
}

func getFAQImportHandler(service FAQImportService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		snapshot, err := service.GetStatus(
			ctx.Request.Context(),
			ctx.Param("knowledgeBaseId"),
			ctx.Param("importId"),
		)
		if err != nil {
			writeFAQImportError(ctx, err)
			return
		}
		items := make([]faqImportItemResponse, len(snapshot.Items))
		for index, item := range snapshot.Items {
			items[index] = faqImportItemResponse{
				RowNumber:  item.RowNumber,
				DocumentID: item.DocumentID,
				VersionID:  item.VersionID,
				Status:     string(item.Status),
				ErrorCode:  item.ErrorCode,
			}
		}
		ctx.JSON(http.StatusOK, faqImportStatusResponse{
			ID:         snapshot.ID,
			SourceName: snapshot.SourceName,
			Status:     string(snapshot.Status),
			TotalRows:  snapshot.TotalRows,
			ReadyRows:  snapshot.ReadyRows,
			FailedRows: snapshot.FailedRows,
			Items:      items,
			CreatedAt:  snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}

func writeKnowledgeBaseError(ctx *gin.Context, err error) {
	var failure *knowledgebase.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_knowledge_base":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "knowledge base is invalid")
	case "knowledge_base_conflict":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "knowledge base already exists")
	default:
		writeAPIError(ctx, http.StatusServiceUnavailable, failure.Code, "knowledge base cannot be created")
	}
}

func writeFAQImportError(ctx *gin.Context, err error) {
	var failure *knowledgeimport.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_knowledge_base_id",
		"invalid_import_source_name",
		"invalid_faq_csv",
		"invalid_faq_import_scope":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "FAQ import request is invalid")
	case "knowledge_base_not_found", "faq_import_not_found":
		writeAPIError(ctx, http.StatusNotFound, failure.Code, "FAQ import was not found")
	default:
		message := "FAQ import is temporarily unavailable"
		if !failure.RetryAllowed {
			message = "FAQ import request failed"
		}
		writeAPIError(ctx, http.StatusServiceUnavailable, failure.Code, message)
	}
}
