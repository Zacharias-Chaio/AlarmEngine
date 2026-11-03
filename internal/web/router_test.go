package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"alarmengine/internal/api"
)

// 未匹配的 API 路径返回统一 JSON envelope；静态资源禁止缓存。
func TestRouterFallbacks(t *testing.T) {
	router := NewRouter(&api.Server{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 API 应 404，实际 %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("未知 API 应返回 JSON envelope，实际 %q", contentType)
	}
	if !strings.Contains(rec.Body.String(), `"code":404`) {
		t.Fatalf("错误响应应带 code 字段: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/js/engine.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("静态资源应 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("静态资源应禁止缓存，实际 Cache-Control=%q", got)
	}
}
