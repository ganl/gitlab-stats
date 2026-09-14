//go:build ignore

// 本文件是一个独立的 GitLab API 模拟服务，仅用于本地端到端冒烟测试：
//
//	go run mockgitlab.go            # 监听 :9999
//
// 它只实现只读接口，用于在没有可用 GitLab Token 的情况下验证
// 「后台定时统计 → 快照落盘 → HTTP 读取 → 进度上报」全链路。
// 不参与正式构建（go:build ignore），因此不会进入产物。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const totalProjects = 120 // 故意超过单页 100 条，用于验证分页

func main() {
	addr := ":9999"
	log.Printf("模拟 GitLab 服务启动: http://localhost%s/api/v4", addr)
	log.Printf("项目总数: %d（用于验证分页与进度上报）", totalProjects)

	http.HandleFunc("/api/v4/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": "16.0.0-mock", "revision": "mock"})
	})

	http.HandleFunc("/api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		if token := r.Header.Get("PRIVATE-TOKEN"); token == "" {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		page := atoiDefault(r.URL.Query().Get("page"), 1)

		projects := make([]map[string]interface{}, 0, 100)
		start := (page - 1) * 100
		for i := start; i < start+100 && i < totalProjects; i++ {
			projects = append(projects, map[string]interface{}{
				"id":   1000 + i,
				"name": fmt.Sprintf("mock-project-%03d", i),
			})
		}
		writeJSON(w, projects)
	})

	http.HandleFunc("/api/v4/users", func(w http.ResponseWriter, r *http.Request) {
		if username := r.URL.Query().Get("username"); username != "" {
			for _, u := range mockUsers {
				if strings.EqualFold(u["username"].(string), username) {
					writeJSON(w, []map[string]interface{}{u})
					return
				}
			}
			writeJSON(w, []map[string]interface{}{})
			return
		}
		writeJSON(w, mockUsers)
	})

	http.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		page := atoiDefault(r.URL.Query().Get("page"), 1)

		switch {
		case strings.HasSuffix(path, "/repository/commits"):
			writeJSON(w, mockCommitsPage(path, page))
		case strings.HasSuffix(path, "/merge_requests"):
			writeJSON(w, mockMRsPage(path, page))
		default:
			http.NotFound(w, r)
		}
	})

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("模拟服务启动失败: %v", err)
	}
}

var mockUsers = []map[string]interface{}{
	{"id": 1, "name": "张三", "username": "zhangsan", "email": "zhangsan@example.com"},
	{"id": 2, "name": "李四", "username": "lisi", "email": "lisi@example.com"},
	{"id": 3, "name": "王五", "username": "wangwu", "email": "wangwu@example.com"},
	{"id": 4, "name": "赵六", "username": "zhaoliu", "email": "zhaoliu@example.com"},
}

// mockCommitsPage 为每个项目生成 3 条提交（分页后每页返回全部）。
func mockCommitsPage(path string, page int) []map[string]interface{} {
	if page > 1 {
		return []map[string]interface{}{}
	}

	id := projectID(path)
	now := time.Now()

	// 参数化：不同项目给出不同规模的提交，使排行有区分度。
	base := id % 7

	return []map[string]interface{}{
		commitJSON(fmt.Sprintf("c%d-1", id), now.AddDate(0, 0, -1), "张三", "zhangsan@example.com", 100+base*10, 10),
		commitJSON(fmt.Sprintf("c%d-2", id), now.AddDate(0, 0, -3), "李四", "lisi@example.com", 60+base*5, 5),
		commitJSON(fmt.Sprintf("c%d-3", id), now.AddDate(0, 0, -5), "张三", "zhangsan@example.com", 40, 8),
		// 模拟 GitLab 未返回 stats 的提交（如合并提交）。
		{
			"id":           fmt.Sprintf("c%d-4", id),
			"created_at":   now.AddDate(0, 0, -2).Format(time.RFC3339),
			"author_name":  "李四",
			"author_email": "lisi@example.com",
		},
	}
}

func mockMRsPage(path string, page int) []map[string]interface{} {
	if page > 1 {
		return []map[string]interface{}{}
	}

	id := projectID(path)
	now := time.Now()
	merged := now.AddDate(0, 0, -2)

	return []map[string]interface{}{
		{
			"id":         id*10 + 1,
			"state":      "merged",
			"created_at": now.AddDate(0, 0, -6).Format(time.RFC3339),
			"merged_at":  merged.Format(time.RFC3339),
			"author":     map[string]interface{}{"id": 1, "name": "张三", "username": "zhangsan"},
		},
		{
			"id":         id*10 + 2,
			"state":      "opened",
			"created_at": now.AddDate(0, 0, -1).Format(time.RFC3339),
			"merged_at":  nil,
			"author":     map[string]interface{}{"id": 2, "name": "李四", "username": "lisi"},
		},
		{
			"id":         id*10 + 3,
			"state":      "closed",
			"created_at": now.AddDate(0, 0, -4).Format(time.RFC3339),
			"merged_at":  nil,
			"author":     map[string]interface{}{"id": 1, "name": "张三", "username": "zhangsan"},
		},
	}
}

func commitJSON(id string, at time.Time, name, email string, additions, deletions int) map[string]interface{} {
	return map[string]interface{}{
		"id":           id,
		"created_at":   at.Format(time.RFC3339),
		"author_name":  name,
		"author_email": email,
		"stats":        map[string]int{"additions": additions, "deletions": deletions},
	}
}

func projectID(path string) int {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "projects" && i+1 < len(parts) {
			return atoiDefault(parts[i+1], 0)
		}
	}
	return 0
}

func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
