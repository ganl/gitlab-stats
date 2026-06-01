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
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Printf("配置加载失败: %v\n", err)
		os.Exit(1)
	}

	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		fmt.Printf("模板加载失败: %v\n", err)
		os.Exit(1)
	}

	cache := NewCache(cfg.CacheEnabled, cfg.CacheTTL)
	gl := NewGitLabClient(cfg, cache)
	h := NewHandler(gl, tmpl, cfg)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: h.Router(),
	}

	go func() {
		fmt.Printf("🚀 GitLab 统计服务启动中...\n")
		fmt.Printf("📊 统计范围: 整个 GitLab 实例\n")
		fmt.Printf("🌐 访问地址: http://localhost:%d\n", cfg.Port)
		fmt.Printf("⚙️  最大并发: %d\n", cfg.MaxConcurrent)
		fmt.Printf("⏱️  缓存: %v (TTL: %s)\n", cfg.CacheEnabled, time.Duration(cfg.CacheTTL))
		fmt.Printf("按 Ctrl+C 停止服务\n\n")

		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			fmt.Printf("服务器错误: %v\n", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\n正在关闭服务...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		fmt.Printf("服务关闭错误: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("服务已关闭")
}
