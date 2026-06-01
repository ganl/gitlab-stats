package main

import (
	"os"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	tempFile, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	configData := `{
		"gitlab_url": "https://gitlab.com",
		"token": "test-token",
		"port": 9090,
		"max_concurrent": 50,
		"request_timeout": "30s",
		"cache_enabled": true,
		"cache_ttl": "10m"
	}`
	if _, err := tempFile.Write([]byte(configData)); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tempFile.Close()

	bakExists := false
	if _, err := os.Stat("config.json"); err == nil {
		os.Rename("config.json", "config.json.bak")
		bakExists = true
	}
	defer func() {
		if bakExists {
			os.Rename("config.json.bak", "config.json")
		} else if _, err := os.Stat("config.json"); err == nil {
			os.Remove("config.json")
		}
	}()

	if err := os.Rename(tempFile.Name(), "config.json"); err != nil {
		t.Fatalf("Failed to rename temp file: %v", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.GitLabURL != "https://gitlab.com" {
		t.Errorf("expected GitLabURL=https://gitlab.com, got %s", cfg.GitLabURL)
	}
	if cfg.Port != 9090 {
		t.Errorf("expected Port=9090, got %d", cfg.Port)
	}
	if cfg.MaxConcurrent != 50 {
		t.Errorf("expected MaxConcurrent=50, got %d", cfg.MaxConcurrent)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	bakExists := false
	if _, err := os.Stat("config.json"); err == nil {
		os.Rename("config.json", "config.json.bak")
		bakExists = true
	}
	defer func() {
		if bakExists {
			os.Rename("config.json.bak", "config.json")
		}
	}()

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for missing config file")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	tempFile, err := os.CreateTemp("", "invalid-config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.Write([]byte("invalid json")); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tempFile.Close()

	bakExists := false
	if _, err := os.Stat("config.json"); err == nil {
		os.Rename("config.json", "config.json.bak")
		bakExists = true
	}
	defer func() {
		if bakExists {
			os.Rename("config.json.bak", "config.json")
		} else if _, err := os.Stat("config.json"); err == nil {
			os.Remove("config.json")
		}
	}()

	if err := os.Rename(tempFile.Name(), "config.json"); err != nil {
		t.Fatalf("Failed to rename temp file: %v", err)
	}

	_, err = LoadConfig()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
