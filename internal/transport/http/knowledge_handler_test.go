package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-chat/internal/application/knowledgebase"
	"agent-chat/internal/application/knowledgeimport"
	domain "agent-chat/internal/domain/knowledge"
)

type fakeKnowledgeBaseCreator struct {
	request knowledgebase.CreateRequest
	result  knowledgebase.CreateResult
	err     error
}

func (creator *fakeKnowledgeBaseCreator) Create(
	_ context.Context,
	request knowledgebase.CreateRequest,
) (knowledgebase.CreateResult, error) {
	creator.request = request
	return creator.result, creator.err
}

type fakeFAQImportService struct {
	request  knowledgeimport.ImportRequest
	result   knowledgeimport.ImportResult
	snapshot domain.FAQImportSnapshot
	err      error
}

func (service *fakeFAQImportService) ImportFAQs(
	_ context.Context,
	request knowledgeimport.ImportRequest,
) (knowledgeimport.ImportResult, error) {
	service.request = request
	return service.result, service.err
}

func (service *fakeFAQImportService) GetStatus(
	_ context.Context,
	_ string,
	_ string,
) (domain.FAQImportSnapshot, error) {
	return service.snapshot, service.err
}

func TestCreateKnowledgeBaseAPI(t *testing.T) {
	creator := &fakeKnowledgeBaseCreator{
		result: knowledgebase.CreateResult{
			ID:          "kb_1",
			Name:        "产品帮助中心",
			Description: "SaaS FAQ",
			Status:      domain.BaseStatusActive,
		},
	}
	router := newKnowledgeTestRouter(creator, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/knowledge-bases",
		bytes.NewBufferString(`{"name":"产品帮助中心","description":"SaaS FAQ"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(adminIDHeader, "admin-demo")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if creator.request.Name != "产品帮助中心" {
		t.Fatalf("unexpected request: %#v", creator.request)
	}
	var payload knowledgeBaseResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != "kb_1" || payload.Status != "active" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestKnowledgeAdminAPIRequiresIdentity(t *testing.T) {
	router := newKnowledgeTestRouter(&fakeKnowledgeBaseCreator{}, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/knowledge-bases",
		bytes.NewBufferString(`{"name":"知识库"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestImportFAQAPI(t *testing.T) {
	importService := &fakeFAQImportService{
		result: knowledgeimport.ImportResult{
			ImportID:  "imp_1",
			Status:    domain.IndexStatusPending,
			TotalRows: 1,
		},
	}
	router := newKnowledgeTestRouter(nil, importService)
	body, contentType := faqMultipartBody(t, "faq.csv", "question,answer\n问题,答案\n")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/knowledge-bases/kb_1/faq-imports",
		body,
	)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(adminIDHeader, "admin-demo")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if importService.request.KnowledgeBaseID != "kb_1" ||
		importService.request.SourceName != "faq.csv" ||
		string(importService.request.Content) != "question,answer\n问题,答案\n" {
		t.Fatalf("unexpected import request: %#v", importService.request)
	}
}

func TestGetFAQImportStatusAPI(t *testing.T) {
	createdAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	importService := &fakeFAQImportService{
		snapshot: domain.FAQImportSnapshot{
			ID:         "imp_1",
			SourceName: "faq.csv",
			Status:     domain.IndexStatusFailed,
			TotalRows:  2,
			ReadyRows:  1,
			FailedRows: 1,
			CreatedAt:  createdAt,
			Items: []domain.FAQImportItemStatus{
				{
					RowNumber:  2,
					DocumentID: "doc_1",
					VersionID:  "ver_1",
					Status:     domain.IndexStatusReady,
				},
				{
					RowNumber:  3,
					DocumentID: "doc_2",
					VersionID:  "ver_2",
					Status:     domain.IndexStatusFailed,
					ErrorCode:  "embedding_unavailable",
				},
			},
		},
	}
	router := newKnowledgeTestRouter(nil, importService)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/knowledge-bases/kb_1/faq-imports/imp_1",
		nil,
	)
	request.Header.Set(adminIDHeader, "admin-demo")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var payload faqImportStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "failed" ||
		payload.Items[1].ErrorCode != "embedding_unavailable" ||
		payload.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func newKnowledgeTestRouter(
	baseCreator KnowledgeBaseCreator,
	importService FAQImportService,
) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(RouterOptions{
		Logger:              logger,
		Database:            fakeDatabaseHealth{},
		DatabasePingTimeout: time.Second,
		Environment:         "test",
		KnowledgeBase:       baseCreator,
		FAQImport:           importService,
	})
}

func faqMultipartBody(
	t *testing.T,
	filename string,
	content string,
) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}
