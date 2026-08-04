package httptransport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"agent-chat/internal/application/knowledgedocument"

	"github.com/gin-gonic/gin"
)

const maxMarkdownJSONBodyBytes = knowledgedocument.MaxContentBytes + (64 << 10)

// MarkdownDocumentService 定义 Markdown 管理 Handler 依赖的 Application 用例。
type MarkdownDocumentService interface {
	Create(context.Context, knowledgedocument.CreateRequest) (knowledgedocument.DocumentDetail, error)
	CreateVersion(context.Context, knowledgedocument.CreateVersionRequest) (knowledgedocument.DocumentDetail, error)
	List(context.Context, string) ([]knowledgedocument.DocumentItem, error)
	Get(context.Context, string, string) (knowledgedocument.DocumentDetail, error)
	Retry(context.Context, string, string, string) (knowledgedocument.DocumentDetail, error)
}

type markdownDocumentRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	SourceURL string `json:"sourceUrl"`
}

type markdownVersionRequest struct {
	Content string `json:"content"`
}

type markdownVersionResponse struct {
	ID        string  `json:"id"`
	Number    int     `json:"number"`
	Status    string  `json:"status"`
	ErrorCode string  `json:"errorCode,omitempty"`
	Active    bool    `json:"active"`
	CreatedAt string  `json:"createdAt"`
	IndexedAt *string `json:"indexedAt,omitempty"`
}

type markdownDocumentResponse struct {
	ID              string                    `json:"id"`
	KnowledgeBaseID string                    `json:"knowledgeBaseId"`
	Title           string                    `json:"title"`
	SourceURL       string                    `json:"sourceUrl,omitempty"`
	ActiveVersionID string                    `json:"activeVersionId,omitempty"`
	LatestVersion   int                       `json:"latestVersion"`
	LatestContent   string                    `json:"latestContent,omitempty"`
	Versions        []markdownVersionResponse `json:"versions"`
	CreatedAt       string                    `json:"createdAt"`
	UpdatedAt       string                    `json:"updatedAt"`
}

// registerMarkdownRoutes 注册知识库作用域内的 Markdown 文档管理接口。
func registerMarkdownRoutes(router *gin.Engine, service MarkdownDocumentService) {
	if service == nil {
		return
	}
	admin := router.Group("/api/v1/admin/knowledge-bases/:knowledgeBaseId/documents")
	admin.GET("", listMarkdownDocumentsHandler(service))
	admin.POST("", createMarkdownDocumentHandler(service))
	admin.GET("/:documentId", getMarkdownDocumentHandler(service))
	admin.POST("/:documentId/versions", createMarkdownVersionHandler(service))
	admin.POST("/:documentId/versions/:versionId/retry", retryMarkdownVersionHandler(service))
}

func createMarkdownDocumentHandler(service MarkdownDocumentService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		var request markdownDocumentRequest
		if err := decodeJSONBodyWithLimit(ctx, &request, maxMarkdownJSONBodyBytes); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.Create(ctx.Request.Context(), knowledgedocument.CreateRequest{
			KnowledgeBaseID: ctx.Param("knowledgeBaseId"), Title: request.Title,
			Content: request.Content, SourceURL: request.SourceURL,
		})
		if err != nil {
			writeMarkdownError(ctx, err)
			return
		}
		ctx.JSON(http.StatusCreated, mapMarkdownDetail(result, true))
	}
}

func createMarkdownVersionHandler(service MarkdownDocumentService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		var request markdownVersionRequest
		if err := decodeJSONBodyWithLimit(ctx, &request, maxMarkdownJSONBodyBytes); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.CreateVersion(ctx.Request.Context(), knowledgedocument.CreateVersionRequest{
			KnowledgeBaseID: ctx.Param("knowledgeBaseId"),
			DocumentID:      ctx.Param("documentId"), Content: request.Content,
		})
		if err != nil {
			writeMarkdownError(ctx, err)
			return
		}
		ctx.JSON(http.StatusAccepted, mapMarkdownDetail(result, true))
	}
}

func listMarkdownDocumentsHandler(service MarkdownDocumentService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		items, err := service.List(ctx.Request.Context(), ctx.Param("knowledgeBaseId"))
		if err != nil {
			writeMarkdownError(ctx, err)
			return
		}
		response := make([]markdownDocumentResponse, len(items))
		for index := range items {
			response[index] = mapMarkdownItem(items[index])
		}
		ctx.JSON(http.StatusOK, gin.H{"items": response})
	}
}

func getMarkdownDocumentHandler(service MarkdownDocumentService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		result, err := service.Get(ctx.Request.Context(), ctx.Param("knowledgeBaseId"), ctx.Param("documentId"))
		if err != nil {
			writeMarkdownError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapMarkdownDetail(result, true))
	}
}

func retryMarkdownVersionHandler(service MarkdownDocumentService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		result, err := service.Retry(ctx.Request.Context(), ctx.Param("knowledgeBaseId"),
			ctx.Param("documentId"), ctx.Param("versionId"))
		if err != nil {
			writeMarkdownError(ctx, err)
			return
		}
		ctx.JSON(http.StatusAccepted, mapMarkdownDetail(result, true))
	}
}

func mapMarkdownDetail(detail knowledgedocument.DocumentDetail, includeContent bool) markdownDocumentResponse {
	response := mapMarkdownItem(detail.DocumentItem)
	if includeContent {
		response.LatestContent = detail.LatestContent
	}
	return response
}

func mapMarkdownItem(item knowledgedocument.DocumentItem) markdownDocumentResponse {
	versions := make([]markdownVersionResponse, len(item.Versions))
	for index, version := range item.Versions {
		var indexedAt *string
		if version.IndexedAt != nil {
			formatted := version.IndexedAt.UTC().Format(time.RFC3339Nano)
			indexedAt = &formatted
		}
		versions[index] = markdownVersionResponse{
			ID: version.ID, Number: version.Number, Status: string(version.Status),
			ErrorCode: version.ErrorCode, Active: version.Active,
			CreatedAt: version.CreatedAt.UTC().Format(time.RFC3339Nano), IndexedAt: indexedAt,
		}
	}
	return markdownDocumentResponse{
		ID: item.ID, KnowledgeBaseID: item.KnowledgeBaseID, Title: item.Title,
		SourceURL: item.SourceURL, ActiveVersionID: item.ActiveVersionID,
		LatestVersion: item.LatestVersion, Versions: versions,
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func writeMarkdownError(ctx *gin.Context, err error) {
	var failure *knowledgedocument.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_knowledge_base_id", "invalid_document_id", "invalid_version_id",
		"invalid_markdown_document", "invalid_markdown_content", "invalid_source_url":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "Markdown document request is invalid")
	case "markdown_document_not_found":
		writeAPIError(ctx, http.StatusNotFound, failure.Code, "Markdown document was not found")
	case "markdown_document_conflict":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "Markdown content is unchanged or conflicts with an existing version")
	case "markdown_version_not_retryable":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "Markdown version is not retryable")
	default:
		writeAPIError(ctx, http.StatusServiceUnavailable, failure.Code, "Markdown document service is temporarily unavailable")
	}
}
