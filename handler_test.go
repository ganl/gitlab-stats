package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	handler := &Handler{}
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
	handler := &Handler{}
	testTime, _ := time.Parse("2006-01-02", "2024-01-01")
	key := handler.formatKey(testTime, "day")
	expected := "2024-01-01"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}

func TestFormatKey_Week(t *testing.T) {
	handler := &Handler{}
	testTime, _ := time.Parse("2006-01-02", "2024-01-01")
	key := handler.formatKey(testTime, "week")
	if !strings.HasPrefix(key, "2024-W") {
		t.Errorf("expected week format, got %s", key)
	}
}

func TestFormatKey_Month(t *testing.T) {
	handler := &Handler{}
	testTime, _ := time.Parse("2006-01-02", "2024-01-15")
	key := handler.formatKey(testTime, "month")
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

func TestJsonError(t *testing.T) {
	handler := &Handler{}
	rr := httptest.NewRecorder()
	handler.jsonError(rr, http.StatusInternalServerError, "test error")

	if status := rr.Code; status != http.StatusInternalServerError {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusInternalServerError)
	}

	expectedHeader := "application/json"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedHeader {
		t.Errorf("handler returned wrong content-type: got %v want %v", contentType, expectedHeader)
	}
}
