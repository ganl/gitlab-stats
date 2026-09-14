package main

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	store := NewStore(t.TempDir())
	handler := &Handler{
		store: store,
		jobs:  NewJobManager(),
	}
	rr := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.healthHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
}

func TestFormatKey_Day(t *testing.T) {
	testTime, _ := time.Parse("2006-01-02", "2024-01-01")
	key := formatKey(testTime, "day")
	expected := "2024-01-01"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}

func TestFormatKey_Week(t *testing.T) {
	testTime, _ := time.Parse("2006-01-02", "2024-01-01")
	key := formatKey(testTime, "week")
	if !strings.HasPrefix(key, "2024-W") {
		t.Errorf("expected week format, got %s", key)
	}
}

func TestFormatKey_Month(t *testing.T) {
	testTime, _ := time.Parse("2006-01-02", "2024-01-15")
	key := formatKey(testTime, "month")
	expected := "2024-01"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}

func TestParseQueryParams_Defaults(t *testing.T) {
	handler := &Handler{}
	req, _ := http.NewRequest("GET", "/", nil)
	period, days := handler.parseQueryParams(req)
	if period != "day" {
		t.Errorf("expected period=day, got %s", period)
	}
	if days != 90 {
		t.Errorf("expected days=90, got %d", days)
	}
}

func TestParseQueryParams_Custom(t *testing.T) {
	handler := &Handler{}
	req, _ := http.NewRequest("GET", "/?period=week&days=30", nil)
	period, days := handler.parseQueryParams(req)
	if period != "week" {
		t.Errorf("expected period=week, got %s", period)
	}
	if days != 30 {
		t.Errorf("expected days=30, got %d", days)
	}
}

// TestParseQueryParams_InvalidPeriodFallsBack 非法周期必须回落到 day，避免前端传错参数导致聚合异常。
func TestParseQueryParams_InvalidPeriodFallsBack(t *testing.T) {
	handler := &Handler{}
	req, _ := http.NewRequest("GET", "/?period=quarter&days=abc", nil)
	period, days := handler.parseQueryParams(req)
	if period != "day" {
		t.Errorf("expected period=day, got %s", period)
	}
	if days != 90 {
		t.Errorf("expected days=90 fallback, got %d", days)
	}
}

// TestParseQueryParams_ShortRange 短区间（如最近 7 天）不得被默认值或下界夹取。
// 展示范围只有上界（快照窗口），没有下界，前端新增更短的选项无需改后端。
func TestParseQueryParams_ShortRange(t *testing.T) {
	handler := &Handler{}
	for _, days := range []string{"1", "7", "14"} {
		req, _ := http.NewRequest("GET", "/?period=day&days="+days, nil)
		_, got := handler.parseQueryParams(req)
		want, _ := strconv.Atoi(days)
		if got != want {
			t.Errorf("days=%s 应原样保留 %d，实际 %d", days, want, got)
		}
	}
}

// TestParseQueryParams_ClampToSnapshotWindow 展示窗口不得超过快照窗口。
func TestParseQueryParams_ClampToSnapshotWindow(t *testing.T) {
	handler := &Handler{}
	req, _ := http.NewRequest("GET", "/?days=9999", nil)
	_, days := handler.parseQueryParams(req)
	if days != WindowDays {
		t.Errorf("expected days clamped to %d, got %d", WindowDays, days)
	}
}

// TestRequireSnapshot_NoData 无快照时必须返回 503 且带结构化错误，不能返回空图表。
func TestRequireSnapshot_NoData(t *testing.T) {
	handler := &Handler{
		store: NewStore(t.TempDir()),
		jobs:  NewJobManager(),
	}
	rr := httptest.NewRecorder()
	if _, ok := handler.requireSnapshot(rr); ok {
		t.Fatal("expected snapshot to be unavailable")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if payload["error"] == nil {
		t.Error("expected error field in response")
	}
}

// TestRequireSnapshot_RunningReturns202 统计进行中且无缓存时返回 202，前端据此显示进度。
func TestRequireSnapshot_RunningReturns202(t *testing.T) {
	handler := &Handler{
		store: NewStore(t.TempDir()),
		jobs:  NewJobManager(),
	}
	// 用永不返回的 collector 模拟长时间运行的任务。
	handler.jobs.SetCollector(func(ctx context.Context, onProgress func(int, int)) (*Snapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := handler.jobs.Trigger("test"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	rr := httptest.NewRecorder()
	if _, ok := handler.requireSnapshot(rr); ok {
		t.Fatal("expected snapshot to be unavailable")
	}
	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 while job running, got %d", rr.Code)
	}
}

func TestJsonError(t *testing.T) {
	handler := &Handler{}
	rr := httptest.NewRecorder()
	handler.jsonError(rr, http.StatusInternalServerError, "test error")

	if status := rr.Code; status != http.StatusInternalServerError {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusInternalServerError)
	}

	expectedHeader := "application/json; charset=utf-8"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedHeader {
		t.Errorf("handler returned wrong content-type: got %v want %v", contentType, expectedHeader)
	}
}

// TestIndexHandler_NoStore 页面响应必须禁止缓存。
//
// 模板内嵌在可执行文件里，靠重新编译升级。若浏览器沿用缓存副本，
// 服务端修复了而界面仍是旧行为，排查成本极高——这类问题必须从源头堵死。
func TestIndexHandler_NoStore(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		t.Fatalf("解析模板失败: %v", err)
	}
	handler := &Handler{tmpl: tmpl}
	rr := httptest.NewRecorder()

	handler.indexHandler(rr, httptest.NewRequest("GET", "/", nil))

	if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("页面响应必须禁止缓存，实际 Cache-Control = %q", got)
	}
	if got := rr.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("HTTP/1.0 兼容头缺失，实际 Pragma = %q", got)
	}
}

// TestWriteJSON_NoStore 统计数据接口不得被任何中间层缓存。
func TestWriteJSON_NoStore(t *testing.T) {
	rr := httptest.NewRecorder()

	writeJSON(rr, http.StatusOK, map[string]int{"commits": 1})

	if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("接口响应必须禁止缓存，实际 Cache-Control = %q", got)
	}
}

func TestStaticETags_AreContentHashes(t *testing.T) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("挂载静态资源失败: %v", err)
	}

	etags := staticETags(sub)
	if len(etags) == 0 {
		t.Fatal("内嵌静态资源不应为空")
	}

	for path, tag := range etags {
		if len(tag) < 3 || !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
			t.Errorf("ETag 应为带引号的强校验符，实际 %s = %q", path, tag)
		}
	}
}

// TestWithStaticCache_ServesNotModified 命中 If-None-Match 时返回 304。
//
// 205 KB 的 Chart.js 每次全量重传并不必要；协商缓存能让版本未变时
// 只走一次头部往返，同时保证内容变了客户端立刻拿到新副本。
func TestWithStaticCache_ServesNotModified(t *testing.T) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("挂载静态资源失败: %v", err)
	}
	handler := withStaticCache(staticETags(sub),
		http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	const assetURL = "/static/vendor/chart.umd.min.js"

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest("GET", assetURL, nil))

	if first.Code != http.StatusOK {
		t.Fatalf("首次请求应返回 200，实际 %d", first.Code)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("首次请求必须下发 ETag")
	}

	second := httptest.NewRequest("GET", assetURL, nil)
	second.Header.Set("If-None-Match", tag)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, second)

	if rr.Code != http.StatusNotModified {
		t.Errorf("ETag 命中应返回 304，实际 %d", rr.Code)
	}
}
