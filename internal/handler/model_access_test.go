package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func TestModelsHandlerFiltersHiddenModels(t *testing.T) {
	_, access := testAccessManagers(t)
	visible := false
	if _, err := access.Upsert("claude-sonnet-4.6", modelaccess.Patch{Visible: &visible}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ModelsHandler(access).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Data {
		if item.ID == "claude-sonnet-4.6" {
			t.Fatalf("hidden model was returned: %+v", body.Data)
		}
	}
}

func TestChatRejectsDisabledModelBeforeReserve(t *testing.T) {
	am, access := testAccessManagers(t)
	disabled := false
	if _, err := access.Upsert("claude-sonnet-4.6", modelaccess.Patch{Enabled: &disabled, UnsupportedReason: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	ChatCompletionsHandler(nil, am, nil, nil, access).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "maintenance") || !strings.Contains(rec.Body.String(), "model_blocked") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoutesRejectHiddenModelBeforeReserve(t *testing.T) {
	am, access := testAccessManagers(t)
	visible := false
	if _, err := access.Upsert("claude-sonnet-4.6", modelaccess.Patch{Visible: &visible}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	ChatCompletionsHandler(nil, am, nil, nil, access).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "hidden") || !strings.Contains(rec.Body.String(), "model_blocked") {
		t.Fatalf("chat status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMessagesAndResponsesRejectDisabledModel(t *testing.T) {
	am, access := testAccessManagers(t)
	disabled := false
	if _, err := access.Upsert("claude-sonnet-4.6", modelaccess.Patch{Enabled: &disabled, UnsupportedReason: "disabled for test"}); err != nil {
		t.Fatal(err)
	}
	msgReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`))
	msgRec := httptest.NewRecorder()
	MessagesHandler(am, nil, nil, access).ServeHTTP(msgRec, msgReq)
	if msgRec.Code != http.StatusForbidden || !strings.Contains(msgRec.Body.String(), "disabled for test") || !strings.Contains(msgRec.Body.String(), "model_blocked") {
		t.Fatalf("messages status=%d body=%s", msgRec.Code, msgRec.Body.String())
	}

	respReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claude-sonnet-4.6","input":"hi"}`))
	respRec := httptest.NewRecorder()
	ResponsesHandler(am, nil, nil, access).ServeHTTP(respRec, respReq)
	if respRec.Code != http.StatusForbidden || !strings.Contains(respRec.Body.String(), "disabled for test") || !strings.Contains(respRec.Body.String(), "model_blocked") {
		t.Fatalf("responses status=%d body=%s", respRec.Code, respRec.Body.String())
	}
}

func TestRoutesRejectUnsupportedCatalogModelBeforeReserve(t *testing.T) {
	am, access := testAccessManagers(t)

	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	chatRec := httptest.NewRecorder()
	ChatCompletionsHandler(nil, am, nil, nil, access).ServeHTTP(chatRec, chatReq)
	if chatRec.Code != http.StatusBadRequest || !strings.Contains(chatRec.Body.String(), "unsupported on direct backend") {
		t.Fatalf("chat status=%d body=%s", chatRec.Code, chatRec.Body.String())
	}

	msgReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	msgRec := httptest.NewRecorder()
	MessagesHandler(am, nil, nil, access).ServeHTTP(msgRec, msgReq)
	if msgRec.Code != http.StatusBadRequest || !strings.Contains(msgRec.Body.String(), "unsupported on direct backend") {
		t.Fatalf("messages status=%d body=%s", msgRec.Code, msgRec.Body.String())
	}

	respReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hi"}`))
	respRec := httptest.NewRecorder()
	ResponsesHandler(am, nil, nil, access).ServeHTTP(respRec, respReq)
	if respRec.Code != http.StatusBadRequest || !strings.Contains(respRec.Body.String(), "unsupported on direct backend") {
		t.Fatalf("responses status=%d body=%s", respRec.Code, respRec.Body.String())
	}
}

func TestAuthModelAccessHandler(t *testing.T) {
	_, access := testAccessManagers(t)
	body := `{"visible":false,"enabled":false,"deprecated":true,"unsupported_reason":"off","notes":"manual"}`
	req := httptest.NewRequest(http.MethodPatch, "/auth/models/claude-sonnet-4.6/access", strings.NewReader(body))
	rec := httptest.NewRecorder()
	AuthModelAccessHandler(access).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got modelaccess.Access
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Visible || got.Enabled || !got.Deprecated || got.UnsupportedReason != "off" {
		t.Fatalf("access=%+v", got)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/auth/models/claude-sonnet-4.6/access", nil)
	delRec := httptest.NewRecorder()
	AuthModelAccessHandler(access).ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", delRec.Code, delRec.Body.String())
	}
	if ok, reason := access.IsEnabled("claude-sonnet-4.6"); !ok || reason != "" {
		t.Fatalf("after reset enabled=%v reason=%q", ok, reason)
	}
}

func testAccessManagers(t *testing.T) (*account.Manager, *modelaccess.Manager) {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return account.NewManager(sqliteStore), modelaccess.NewManager(sqliteStore)
}
