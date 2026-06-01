package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationUnmarshalJSON_String(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{"seconds", `"30s"`, 30 * time.Second},
		{"minutes", `"5m"`, 5 * time.Minute},
		{"hours", `"1h"`, 1 * time.Hour},
		{"milliseconds", `"500ms"`, 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.input), &d); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if time.Duration(d) != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, d)
			}
		})
	}
}

func TestDurationUnmarshalJSON_Number(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{"30 seconds", "30", 30},
		{"0 seconds", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tt.input), &d); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if time.Duration(d) != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, d)
			}
		})
	}
}

func TestDurationUnmarshalJSON_Invalid(t *testing.T) {
	invalidInputs := []string{
		`"invalid"`,
		`"30x"`,
	}

	for _, input := range invalidInputs {
		var d Duration
		if err := json.Unmarshal([]byte(input), &d); err == nil {
			t.Errorf("expected error for input %s, got nil", input)
		}
	}
}

func TestConfigSetDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.SetDefaults()

	if cfg.Port != 8080 {
		t.Errorf("expected Port=8080, got %d", cfg.Port)
	}
	if cfg.MaxConcurrent != 20 {
		t.Errorf("expected MaxConcurrent=20, got %d", cfg.MaxConcurrent)
	}
	if time.Duration(cfg.RequestTimeout) != 30*time.Second {
		t.Errorf("expected RequestTimeout=30s, got %v", cfg.RequestTimeout)
	}
	if time.Duration(cfg.CacheTTL) != 5*time.Minute {
		t.Errorf("expected CacheTTL=5m, got %v", cfg.CacheTTL)
	}
}

func TestConfigValidate_Valid(t *testing.T) {
	cfg := &Config{
		GitLabURL:     "https://gitlab.com",
		Token:         "test-token",
		MaxConcurrent: 50,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

func TestConfigValidate_InvalidURL(t *testing.T) {
	tests := []*Config{
		{GitLabURL: "", Token: "test-token"},
		{GitLabURL: "gitlab.com", Token: "test-token"},
	}

	for _, cfg := range tests {
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for invalid URL")
		}
	}
}

func TestConfigValidate_InvalidToken(t *testing.T) {
	cfg := &Config{
		GitLabURL: "https://gitlab.com",
		Token:     "",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty token")
	}
}

func TestConfigValidate_InvalidConcurrent(t *testing.T) {
	tests := []int{0, 101}

	for _, concurrency := range tests {
		cfg := &Config{
			GitLabURL:     "https://gitlab.com",
			Token:         "test-token",
			MaxConcurrent: concurrency,
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected error for MaxConcurrent=%d", concurrency)
		}
	}
}
