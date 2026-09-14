package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig 在指定目录写入临时 config.json 并切换工作目录。
//
// 原实现把临时文件建在系统 Temp（可能在 C: 盘）再用 os.Rename 移到项目目录（B: 盘），
// 跨盘 rename 在 Windows 上必然失败。这里把临时文件直接建在项目目录内，规避该问题。
func writeConfig(t *testing.T, body string) (restore func()) {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}

	backup := ""
	if _, statErr := os.Stat("config.json"); statErr == nil {
		data, readErr := os.ReadFile("config.json")
		if readErr != nil {
			t.Fatalf("备份 config.json 失败: %v", readErr)
		}
		backup = string(data)
	}

	if err := os.WriteFile("config.json", []byte(body), 0o600); err != nil {
		t.Fatalf("写入 config.json 失败: %v", err)
	}

	return func() {
		if backup != "" {
			_ = os.WriteFile("config.json", []byte(backup), 0o600)
		} else {
			_ = os.Remove("config.json")
		}
		if err := os.Chdir(wd); err != nil {
			t.Logf("恢复工作目录失败: %v", err)
		}
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	restore := writeConfig(t, `{
		"gitlab_url": "https://gitlab.com",
		"token": "test-token",
		"port": 9090,
		"max_concurrent": 50,
		"request_timeout": "30s",
		"cache_enabled": true,
		"cache_ttl": "10m"
	}`)
	defer restore()

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
	// 未配置定时统计相关字段时应填默认值。
	if cfg.DataDir != "data" {
		t.Errorf("expected DataDir=data, got %s", cfg.DataDir)
	}
	if cfg.StatsCron != DefaultStatsCron {
		t.Errorf("expected StatsCron=%s, got %s", DefaultStatsCron, cfg.StatsCron)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("切换工作目录失败: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Logf("恢复工作目录失败: %v", err)
		}
	}()

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for missing config file")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "config.json")); err == nil {
		t.Error("test should not have created config.json in temp dir")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	restore := writeConfig(t, "invalid json")
	defer restore()

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestLoadConfig_RejectsInvalidCron 非法 cron 必须在启动阶段直接失败，而不是静默禁用定时统计。
func TestLoadConfig_RejectsInvalidCron(t *testing.T) {
	restore := writeConfig(t, `{
		"gitlab_url": "https://gitlab.com",
		"token": "test-token",
		"max_concurrent": 20,
		"stats_cron": "not a cron"
	}`)
	defer restore()

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for invalid stats_cron")
	}
}
