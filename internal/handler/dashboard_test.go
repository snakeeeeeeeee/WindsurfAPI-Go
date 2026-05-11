package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardHandlerServesSPAFallbackRoutes(t *testing.T) {
	h := DashboardHandler()
	for _, path := range []string{"/dashboard", "/dashboard/accounts", "/dashboard/scheduler"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, `/dashboard/assets/`) {
			t.Fatalf("%s did not return dashboard SPA index: %s", path, body)
		}
	}
}

