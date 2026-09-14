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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler 只从本地快照读取统计数据，绝不向 GitLab 发起实时查询。
// 这样前端响应时间与 GitLab 规模、网络状况完全解耦。
type Handler struct {
	tmpl  *template.Template
	cfg   *Config
	store *Store
	jobs  *JobManager
}

func NewHandler(cfg *Config, tmpl *template.Template, store *Store, jobs *JobManager) *Handler {
	return &Handler{
		tmpl:  tmpl,
		cfg:   cfg,
		store: store,
		jobs:  jobs,
	}
}

func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.indexHandler)
	mux.HandleFunc("/health", h.healthHandler)
	// 内嵌的前端静态资源（Chart.js）。fs.Sub 把根指向 static/ 子目录，
	// 因此 URL /static/vendor/chart.umd.min.js 能正确映射到
	// 内嵌路径 static/vendor/chart.umd.min.js。
	//
	// 注意：go:embed 提供的 FileInfo.ModTime() 恒为零值，http.FileServer
	// 因此既不下发 Last-Modified 也不下发 ETag。浏览器在这种「无缓存元信息」
	// 的响应上只能按启发式规则自行决定缓存多久，资源更新后客户端可能长期
	// 持有旧副本。这里补上基于内容哈希的 ETag，让协商缓存可靠工作。
	if sub, err := fs.Sub(staticFS, "static"); err != nil {
		log.Printf("[ERROR] 加载静态资源失败: %v", err)
	} else {
		mux.Handle("/static/", withStaticCache(staticETags(sub),
			http.StripPrefix("/static/", http.FileServer(http.FS(sub)))))
	}
	mux.HandleFunc("/api/stats/commit-frequency", h.commitFrequencyHandler)
	mux.HandleFunc("/api/stats/mr-statistics", h.mrStatisticsHandler)
	mux.HandleFunc("/api/stats/code-volume", h.codeVolumeHandler)
	mux.HandleFunc("/api/stats/status", h.statusHandler)
	mux.HandleFunc("/api/stats/refresh", h.refreshHandler)
	return mux
}

func (h *Handler) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.Execute(w, nil); err != nil {
		log.Printf("[ERROR] 渲染页面失败: %v", err)
	}
}

// setNoStore 声明响应不可缓存。
//
// 页面模板与前端逻辑都内嵌在可执行文件里，升级时整包替换；
// 若浏览器继续沿用缓存副本，就会出现「服务端已修复、界面仍是旧行为」
// 这类极难排查的现象。页面体积很小，强制每次取新代价可以忽略。
func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// staticETags 为内嵌静态资源逐一计算内容 ETag。
//
// 用内容哈希而非时间戳：embed 的文件没有可用的修改时间，
// 而内容哈希在任何构建方式下都能准确反映「字节是否变过」。
func staticETags(root fs.FS) map[string]string {
	etags := make(map[string]string, 8)
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // 单个条目不可读不应阻断其余资源
		}
		data, readErr := fs.ReadFile(root, path)
		if readErr != nil {
			log.Printf("[WARN] 计算静态资源 ETag 失败 %s: %v", path, readErr)
			return nil
		}
		sum := sha256.Sum256(data)
		etags[path] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		log.Printf("[WARN] 遍历静态资源失败: %v", err)
	}
	return etags
}

// withStaticCache 为静态资源补上 ETag 与缓存指令。
//
// 只加 no-cache（每次协商）而不加 max-age：资源内容固定，但版本更新时
// 靠 ETag 比对就能立刻失效，304 又省下重新传输整个文件的代价。
// 命中条件请求时，后续的 http.ServeContent 会读取此处设置的 ETag 自行返回 304。
func withStaticCache(etags map[string]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := etags[strings.TrimPrefix(r.URL.Path, "/static/")]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) healthHandler(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.store.Current()
	status := h.jobs.Status()

	payload := map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now().UTC(),
		"job":       status.State,
	}
	if ok && snap != nil {
		payload["snapshot_time"] = snap.GeneratedAt
		payload["snapshot_records"] = len(snap.Daily)
	}
	writeJSON(w, http.StatusOK, payload)
}

// statusHandler 返回快照元信息 + 任务进度，前端据此决定展示缓存数据还是进度条。
func (h *Handler) statusHandler(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"job":      h.jobs.Status(),
		"schedule": h.scheduleInfo(),
	}

	if snap, ok := h.store.Current(); ok && snap != nil {
		resp["snapshot"] = map[string]interface{}{
			"generated_at":     snap.GeneratedAt,
			"duration_ms":      snap.DurationMS,
			"window_days":      snap.WindowDays,
			"records":          len(snap.Daily),
			"projects_scanned": snap.Totals.ProjectsScanned,
			"users_scanned":    snap.Totals.UsersScanned,
			"failed_projects":  len(snap.Totals.ProjectErrors),
			"scope":            snap.Scope,
			"age_seconds":      int(time.Since(snap.GeneratedAt).Seconds()),
			"stale":            snap.Stale(),

			// 覆盖区间与分支口径：前端据此提示「历史仍在回填」，
			// 避免用户在回填完成前把偏低的累计数字当作完整结果。
			"covered_from":      snap.CoveredFrom,
			"scan_all_branches": snap.ScanAllBranches,
		}
	} else {
		resp["snapshot"] = nil
	}

	writeJSON(w, http.StatusOK, resp)
}

// refreshHandler 手动触发一次后台统计。已有任务在跑时返回 409。
func (h *Handler) refreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.jsonError(w, http.StatusMethodNotAllowed, "请使用 POST 触发统计")
		return
	}

	status, err := h.jobs.Trigger("manual")
	if err != nil {
		if err == ErrJobRunning {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":  "统计任务正在执行中",
				"status": status,
			})
			return
		}
		h.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "统计任务已启动",
		"status":  status,
	})
}

func (h *Handler) scheduleInfo() map[string]interface{} {
	info := map[string]interface{}{
		"enabled":     h.cfg.StatsEnabledOrDefault(),
		"cron":        h.cfg.StatsCron,
		"on_start":    h.cfg.StatsOnStartOrDefault(),
		"window_days": WindowDays,
	}
	if sched, err := NewScheduler(h.cfg.StatsCron); err == nil {
		if next, ok := sched.cron.Next(time.Now()); ok {
			info["next_run"] = next
		}
	}
	return info
}

// parseQueryParams 解析展示参数。days 现在只影响内存聚合范围，不会触发 GitLab 请求。
func (h *Handler) parseQueryParams(r *http.Request) (period string, days int) {
	period = r.URL.Query().Get("period")
	if period != "day" && period != "week" && period != "month" {
		period = "day"
	}

	days = 90
	if daysStr := r.URL.Query().Get("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
	}
	// 展示范围不会超过快照窗口，超出部分没有数据。
	if days > WindowDays {
		days = WindowDays
	}

	return period, days
}

func (h *Handler) jsonError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[ERROR] 写入响应失败: %v", err)
	}
}

// requireSnapshot 统一处理「快照尚未生成」的情况，返回 (快照, 是否可用)。
func (h *Handler) requireSnapshot(w http.ResponseWriter) (*Snapshot, bool) {
	snap, ok := h.store.Current()
	if !ok || snap.Empty() {
		status := h.jobs.Status()
		if status.State == JobRunning {
			writeJSON(w, http.StatusAccepted, map[string]interface{}{
				"error":  "统计数据正在生成中，请稍候",
				"status": status,
			})
			return nil, false
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"error":  "暂无统计数据，请先执行一次统计",
			"status": status,
		})
		return nil, false
	}
	return snap, true
}

func (h *Handler) commitFrequencyHandler(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.requireSnapshot(w)
	if !ok {
		return
	}
	period, days := h.parseQueryParams(r)

	result := BuildCommitFrequency(snap, period, days)
	if len(result) == 0 {
		writeJSON(w, http.StatusOK, []CommitFrequency{})
		return
	}

	// 附加快照元数据的响应头，前端无需额外请求即可标注数据新鲜度。
	w.Header().Set("X-Snapshot-Time", snap.GeneratedAt.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) mrStatisticsHandler(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.requireSnapshot(w)
	if !ok {
		return
	}
	period, days := h.parseQueryParams(r)

	stats := BuildMRStatistics(snap, period, days, h.cfg.GitLabURL)
	w.Header().Set("X-Snapshot-Time", snap.GeneratedAt.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) codeVolumeHandler(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.requireSnapshot(w)
	if !ok {
		return
	}
	_, days := h.parseQueryParams(r)

	stats := BuildCodeVolume(snap, days, h.cfg.GitLabURL)
	w.Header().Set("X-Snapshot-Time", snap.GeneratedAt.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, stats)
}

func normalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func buildProfileURL(gitlabURL, username string) string {
	if username == "" {
		return ""
	}
	return strings.TrimSuffix(gitlabURL, "/") + "/" + username
}
