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
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

// staticFS 内嵌前端静态资源（Chart.js）。
//
// 内嵌而非 CDN 引用：本工具常用于内网环境，外网不可达时 CDN 会导致全部图表失效。
// 内嵌后部署只需单个可执行文件，无外部依赖、无网络要求。
//
//go:embed static/*
var staticFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags)

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

	// apiCache 仅用于加速单次统计任务内部的重复 API 调用，TTL 很短的原始响应缓存，
	// 它是采集侧的优化，与前端读取的快照无关。
	apiCache := NewCache(cfg.CacheEnabled, cfg.CacheTTL)
	gl := NewGitLabClient(cfg, apiCache)
	store := NewStore(cfg.DataDir)
	jobs := NewJobManager()
	collector := NewCollector(gl, cfg)
	jobs.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		// 把上一份快照交给采集器：它据此决定增量区间，并把新数据合并进历史。
		prev, _ := store.Current()
		snap, err := collector.Collect(ctx, prev, onProgress)
		if err != nil && !errors.Is(err, ErrSnapshotEmpty) {
			return nil, err
		}
		if err := store.Save(snap); err != nil {
			return nil, err
		}
		return snap, nil
	})

	h := NewHandler(cfg, tmpl, store, jobs)

	printStartupBanner(cfg, store, jobs)

	// 启动时加载已有快照，前端立即可读。
	if snap, ok, err := store.Load(); err != nil {
		log.Printf("[WARN] 加载本地快照失败: %v", err)
	} else if ok {
		if snap.SchemaMismatch() {
			log.Printf("[WARN] 本地快照版本不匹配（磁盘 v%d，当前 v%d），将重新采集",
				snap.SchemaVersion, snapshotSchemaVersion)
		} else {
			log.Printf("[INFO] 已加载本地快照: 生成于 %s，%d 条记录",
				snap.GeneratedAt.Format("2006-01-02 15:04:05"), len(snap.Daily))
		}
	} else {
		log.Printf("[INFO] 未找到本地快照，等待首次统计")
	}

	stop := make(chan struct{})
	scheduler := startScheduler(cfg, jobs, store, stop)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      h.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[ERROR] 服务器错误: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("正在关闭服务...")
	close(stop)

	// 让正在运行的统计任务有时间把快照落盘，避免白跑一轮。
	if jobs.Running() {
		log.Println("等待正在执行的统计任务收尾...")
		if !jobs.Wait(20 * time.Second) {
			log.Println("[WARN] 统计任务未在超时时间内结束，强制退出")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[ERROR] 服务关闭错误: %v", err)
		os.Exit(1)
	}

	_ = scheduler
	log.Println("服务已关闭")
}

func printStartupBanner(cfg *Config, store *Store, jobs *JobManager) {
	log.Printf("GitLab 统计服务启动中...")
	log.Printf("  统计范围: 整个 GitLab 实例（只读，不修改 GitLab 任何数据）")
	log.Printf("  访问地址: http://localhost:%d", cfg.Port)
	log.Printf("  最大并发: %d", cfg.MaxConcurrent)
	log.Printf("  数据目录: %s", store.Dir())
	log.Printf("  统计窗口: 最近 %d 天", WindowDays)
	if cfg.StatsEnabledOrDefault() {
		log.Printf("  定时统计: 已启用 (%s)", cfg.StatsCron)
	} else {
		log.Printf("  定时统计: 已禁用（仅支持手动触发）")
	}
	log.Printf("  启动即统计: %v", cfg.StatsOnStartOrDefault())
	log.Printf("按 Ctrl+C 停止服务")
}

// startScheduler 启动定时统计。返回 nil 表示调度未启用。
func startScheduler(cfg *Config, jobs *JobManager, store *Store, stop <-chan struct{}) *Scheduler {
	if !cfg.StatsEnabledOrDefault() {
		log.Printf("[INFO] 定时统计已禁用")
		return nil
	}

	scheduler, err := NewScheduler(cfg.StatsCron)
	if err != nil {
		log.Printf("[ERROR] 定时统计表达式无效，已禁用定时统计: %v", err)
		return nil
	}

	go func() {
		if cfg.StatsOnStartOrDefault() {
			shouldRun := true
			if snap, ok := store.Current(); ok && !snap.Stale() && !snap.SchemaMismatch() {
				// 快照还很新（例如刚重启过服务），不必立刻再来一轮。
				log.Printf("[INFO] 本地快照仍然新鲜（%s），跳过启动统计",
					snap.GeneratedAt.Format("2006-01-02 15:04:05"))
				shouldRun = false
			}
			if shouldRun {
				if _, err := jobs.Trigger("startup"); err != nil {
					log.Printf("[WARN] 启动统计未能触发: %v", err)
				}
			}
		}
		scheduler.Run(stop, func() {
			if _, err := jobs.Trigger("schedule"); err != nil {
				if errors.Is(err, ErrJobRunning) {
					log.Printf("[WARN] 上一轮统计仍在执行，跳过本次定时触发")
				} else {
					log.Printf("[ERROR] 定时统计触发失败: %v", err)
				}
			}
		})
	}()

	return scheduler
}
