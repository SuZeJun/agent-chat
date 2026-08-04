package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-chat/internal/application/knowledgedocument"
	domain "agent-chat/internal/domain/knowledge"

	"github.com/gin-gonic/gin"
)

type fakeMarkdownDocumentService struct {
	createRequest knowledgedocument.CreateRequest
	result        knowledgedocument.DocumentDetail
	err           error
}

func (service *fakeMarkdownDocumentService) Create(
	_ context.Context,
	request knowledgedocument.CreateRequest,
) (knowledgedocument.DocumentDetail, error) {
	service.createRequest = request
	return service.result, service.err
}

func (service *fakeMarkdownDocumentService) CreateVersion(
	context.Context,
	knowledgedocument.CreateVersionRequest,
) (knowledgedocument.DocumentDetail, error) {
	return service.result, service.err
}

func (service *fakeMarkdownDocumentService) List(
	context.Context,
	string,
) ([]knowledgedocument.DocumentItem, error) {
	return []knowledgedocument.DocumentItem{service.result.DocumentItem}, service.err
}

func (service *fakeMarkdownDocumentService) Get(
	context.Context,
	string,
	string,
) (knowledgedocument.DocumentDetail, error) {
	return service.result, service.err
}

func (service *fakeMarkdownDocumentService) Retry(
	context.Context,
	string,
	string,
	string,
) (knowledgedocument.DocumentDetail, error) {
	return service.result, service.err
}

func TestCreateMarkdownDocumentAPI(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	service := &fakeMarkdownDocumentService{result: knowledgedocument.DocumentDetail{
		DocumentItem: knowledgedocument.DocumentItem{
			ID: "doc_1", KnowledgeBaseID: "kb_1", Title: "API 指南", LatestVersion: 1,
			Versions: []knowledgedocument.VersionItem{{
				ID: "ver_1", Number: 1, Status: domain.IndexStatusPending, CreatedAt: now,
			}},
			CreatedAt: now, UpdatedAt: now,
		},
		LatestContent: "# API",
	}}
	router := gin.New()
	registerMarkdownRoutes(router, service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/knowledge-bases/kb_1/documents",
		bytes.NewBufferString(`{"title":"API 指南","content":"# API","sourceUrl":"https://example.com/api"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(adminIDHeader, "admin-demo")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if service.createRequest.KnowledgeBaseID != "kb_1" || service.createRequest.Content != "# API" {
		t.Fatalf("unexpected request: %#v", service.createRequest)
	}
	var payload markdownDocumentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != "doc_1" || payload.LatestContent != "# API" || payload.Versions[0].Status != "pending" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestMarkdownDocumentAPIRejectsOversizedBodyBeforeService(t *testing.T) {
	service := &fakeMarkdownDocumentService{}
	router := gin.New()
	registerMarkdownRoutes(router, service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/knowledge-bases/kb_1/documents",
		strings.NewReader(`{"title":"large","content":"`+strings.Repeat("a", maxMarkdownJSONBodyBytes)+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(adminIDHeader, "admin-demo")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if service.createRequest.KnowledgeBaseID != "" {
		t.Fatalf("service should not be called: %#v", service.createRequest)
	}
}

func TestMarkdownDocumentAPIRequiresAdminIdentity(t *testing.T) {
	router := gin.New()
	registerMarkdownRoutes(router, &fakeMarkdownDocumentService{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/knowledge-bases/kb_1/documents", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
}
