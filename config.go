package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func LoadConfig() (*Config, error) {
	data, err := os.ReadFile("config.json")
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	cfg.SetDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.GitLabURL == "" {
		return fmt.Errorf("gitlab_url 不能为空")
	}

	if !strings.HasPrefix(c.GitLabURL, "http://") && !strings.HasPrefix(c.GitLabURL, "https://") {
		return fmt.Errorf("gitlab_url 必须以 http:// 或 https:// 开头")
	}

	if c.Token == "" {
		return fmt.Errorf("token 不能为空")
	}

	if c.MaxConcurrent < 1 || c.MaxConcurrent > 100 {
		return fmt.Errorf("max_concurrent 必须在 1 到 100 之间")
	}

	return nil
}
