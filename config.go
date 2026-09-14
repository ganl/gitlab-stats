// Copyright 2026 ganl <769323213@qq.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	// 先补齐默认值再校验，否则未配置的字段（如 stats_cron）会被误判为非法。
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

	// 定时统计开启时，cron 表达式必须可用，否则启动即失败而非静默降级。
	// 直接调用 Validate（未先走 SetDefaults）时按默认表达式校验，避免误报。
	if c.StatsEnabledOrDefault() {
		expr := c.StatsCron
		if expr == "" {
			expr = DefaultStatsCron
		}
		if _, err := ParseCron(expr); err != nil {
			return fmt.Errorf("stats_cron 配置无效: %w", err)
		}
	}

	return nil
}
