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
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Handler struct {
	gl        *GitLabClient
	tmpl      *template.Template
	config    *Config
}

func NewHandler(gl *GitLabClient, tmpl *template.Template, cfg *Config) *Handler {
	return &Handler{
		gl:        gl,
		tmpl:      tmpl,
		config:    cfg,
	}
}

func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.indexHandler)
	mux.HandleFunc("/health", h.healthHandler)
	mux.HandleFunc("/api/stats/commit-frequency", h.commitFrequencyHandler)
	mux.HandleFunc("/api/stats/mr-statistics", h.mrStatisticsHandler)
	mux.HandleFunc("/api/stats/code-volume", h.codeVolumeHandler)
	return mux
}

func (h *Handler) indexHandler(w http.ResponseWriter, r *http.Request) {
	h.tmpl.Execute(w, nil)
}

func (h *Handler) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"timestamp": time.Now().UTC(),
	})
}

func (h *Handler) parseQueryParams(r *http.Request) (period string, days int) {
	period = r.URL.Query().Get("period")
	if period == "" {
		period = "day"
	}
	if period != "day" && period != "week" && period != "month" {
		period = "day"
	}

	daysStr := r.URL.Query().Get("days")
	days = 90
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
	}

	return period, days
}

func (h *Handler) formatKey(t time.Time, period string) string {
	switch period {
	case "week":
		year, week := t.ISOWeek()
		return strconv.Itoa(year) + "-W" + strconv.Itoa(week)
	case "month":
		return t.Format("2006-01")
	default:
		return t.Format("2006-01-02")
	}
}

func (h *Handler) jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (h *Handler) commitFrequencyHandler(w http.ResponseWriter, r *http.Request) {
	period, days := h.parseQueryParams(r)
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		log.Printf("[ERROR] 获取项目列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个项目，开始获取提交数据...", len(projects))

	commitByPeriod := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)
	var failedCount int

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			commits, err := h.gl.GetCommits(p.ID, startDate, endDate)
			if err != nil {
				mu.Lock()
				failedCount++
				mu.Unlock()
				log.Printf("[WARN] 获取项目 [%s] (ID: %d) 提交失败: %v", p.Name, p.ID, err)
				return
			}
			mu.Lock()
			for _, commit := range commits {
				key := h.formatKey(commit.CreatedAt, period)
				commitByPeriod[key]++
			}
			mu.Unlock()
		}(project)
	}

	wg.Wait()

	if failedCount > 0 {
		log.Printf("[WARN] 完成提交频率统计，成功 %d 个项目，失败 %d 个", len(projects)-failedCount, failedCount)
	}

	result := make([]CommitFrequency, 0)
	for date, count := range commitByPeriod {
		result = append(result, CommitFrequency{Date: date, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date < result[j].Date
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) mrStatisticsHandler(w http.ResponseWriter, r *http.Request) {
	period, days := h.parseQueryParams(r)
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		log.Printf("[ERROR] 获取项目列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个项目，开始获取 MR 数据...", len(projects))

	stats := MRStatistics{
		Authors:     make(map[string]int),
		MergedByDay: make(map[string]int),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)
	var failedCount int

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			mrs, err := h.gl.GetMergeRequests(p.ID, startDate, endDate)
			if err != nil {
				mu.Lock()
				failedCount++
				mu.Unlock()
				log.Printf("[WARN] 获取项目 [%s] (ID: %d) MR 失败: %v", p.Name, p.ID, err)
				return
			}
			mu.Lock()
			for _, mr := range mrs {
				stats.Total++
				stats.Authors[mr.Author.Name]++

				switch mr.State {
				case "merged":
					stats.Merged++
					if mr.MergedAt != nil {
						key := h.formatKey(*mr.MergedAt, period)
						stats.MergedByDay[key]++
					}
				case "opened":
					stats.Opened++
				case "closed":
					stats.Closed++
				}
			}
			mu.Unlock()
		}(project)
	}

	wg.Wait()

	if failedCount > 0 {
		log.Printf("[WARN] 完成 MR 统计，成功 %d 个项目，失败 %d 个", len(projects)-failedCount, failedCount)
	}

	sortedAuthors := make([]string, 0, len(stats.Authors))
	for k := range stats.Authors {
		sortedAuthors = append(sortedAuthors, k)
	}
	sort.Slice(sortedAuthors, func(i, j int) bool {
		return stats.Authors[sortedAuthors[i]] > stats.Authors[sortedAuthors[j]]
	})
	if len(sortedAuthors) > 10 {
		sortedAuthors = sortedAuthors[:10]
	}
	topAuthors := make(map[string]int)
	for _, name := range sortedAuthors {
		topAuthors[name] = stats.Authors[name]
	}
	stats.Authors = topAuthors

	type mergedDayItem struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	}
	mergedByDayList := make([]mergedDayItem, 0, len(stats.MergedByDay))
	for date, count := range stats.MergedByDay {
		mergedByDayList = append(mergedByDayList, mergedDayItem{Date: date, Count: count})
	}
	sort.Slice(mergedByDayList, func(i, j int) bool {
		return mergedByDayList[i].Date < mergedByDayList[j].Date
	})
	stats.MergedByDay = make(map[string]int)
	for _, item := range mergedByDayList {
		stats.MergedByDay[item.Date] = item.Count
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (h *Handler) codeVolumeHandler(w http.ResponseWriter, r *http.Request) {
	_, days := h.parseQueryParams(r)
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	stats := CodeVolume{
		TopContributors: make(map[string]ContributorStats),
		InactiveMembers: []string{},
	}

	allUsers, err := h.gl.GetAllUsers()
	if err != nil {
		log.Printf("[ERROR] 获取用户列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取用户失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个用户", len(allUsers))

	userNames := make(map[string]bool)
	for _, user := range allUsers {
		userNames[user.Name] = true
	}

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		log.Printf("[ERROR] 获取项目列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个项目，开始获取代码量数据...", len(projects))

	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)
	var failedCount int

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			commits, err := h.gl.GetCommits(p.ID, startDate, endDate)
			if err != nil {
				mu.Lock()
				failedCount++
				mu.Unlock()
				log.Printf("[WARN] 获取项目 [%s] (ID: %d) 代码量失败: %v", p.Name, p.ID, err)
				return
			}
			mu.Lock()
			for _, commit := range commits {
				stats.TotalCommits++
				stats.TotalAdditions += commit.Stats.Additions
				stats.TotalDeletions += commit.Stats.Deletions

				author := commit.AuthorName
				if author == "" {
					author = "Unknown"
				}

				s := stats.TopContributors[author]
				s.Additions += commit.Stats.Additions
				s.Deletions += commit.Stats.Deletions
				s.Commits++
				stats.TopContributors[author] = s
			}
			mu.Unlock()
		}(project)
	}

	wg.Wait()

	if failedCount > 0 {
		log.Printf("[WARN] 完成代码量统计，成功 %d 个项目，失败 %d 个", len(projects)-failedCount, failedCount)
	}

	// 构建规范化用户名映射（用于大小写不敏感匹配）
	normalizedUsers := make(map[string]string)
	for userName := range userNames {
		normalized := strings.ToLower(strings.TrimSpace(userName))
		normalizedUsers[normalized] = userName
	}

	log.Printf("[DEBUG] GitLab 用户数: %d, 提交作者数: %d", len(userNames), len(stats.TopContributors))

	// 检查每个用户是否有提交（使用大小写不敏感匹配）
	inactiveCount := 0
	for userName := range userNames {
		normalized := strings.ToLower(strings.TrimSpace(userName))
		hasCommit := false

		// 检查原始用户名
		if _, ok := stats.TopContributors[userName]; ok {
			hasCommit = true
		}

		// 检查规范化后的用户名
		if !hasCommit {
			for authorName := range stats.TopContributors {
				normalizedAuthor := strings.ToLower(strings.TrimSpace(authorName))
				if normalized == normalizedAuthor {
					hasCommit = true
					break
				}
			}
		}

		if !hasCommit {
			inactiveCount++
			stats.InactiveMembers = append(stats.InactiveMembers, userName)
		}
	}

	log.Printf("[INFO] 零提交成员数: %d / %d", inactiveCount, len(userNames))
	sort.Strings(stats.InactiveMembers)

	type contributor struct {
		Name  string
		Stats ContributorStats
	}
	contributors := make([]contributor, 0, len(stats.TopContributors))
	for name, stats := range stats.TopContributors {
		contributors = append(contributors, contributor{Name: name, Stats: stats})
	}
	sort.Slice(contributors, func(i, j int) bool {
		return contributors[i].Stats.Commits > contributors[j].Stats.Commits
	})

	topContributors := make(map[string]ContributorStats)
	for i, c := range contributors {
		if i >= 10 {
			break
		}
		topContributors[c.Name] = c.Stats
	}
	stats.TopContributors = topContributors

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
