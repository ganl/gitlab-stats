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

func normalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (h *Handler) matchUser(email, name, username string, emailToUser, nameToUser, usernameToUser map[string]GitLabUser) (GitLabUser, bool) {
	// 1. 优先通过邮箱匹配
	if email != "" {
		if user, ok := emailToUser[normalizeString(email)]; ok {
			return user, true
		}
	}

	// 2. 通过用户名匹配
	if username != "" {
		if user, ok := usernameToUser[normalizeString(username)]; ok {
			return user, true
		}
	}

	// 3. 通过姓名匹配（大小写不敏感）
	if name != "" {
		if user, ok := nameToUser[normalizeString(name)]; ok {
			return user, true
		}
	}

	return GitLabUser{}, false
}

func (h *Handler) buildProfileURL(gitlabURL, username string) string {
	if username == "" {
		return ""
	}
	return strings.TrimSuffix(gitlabURL, "/") + "/" + username
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

	// 获取所有用户用于匹配
	allUsers, err := h.gl.GetAllUsers()
	if err != nil {
		log.Printf("[ERROR] 获取用户列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取用户失败: "+err.Error())
		return
	}

	// 构建多维度用户映射（大小写不敏感）
	emailToUser := make(map[string]GitLabUser)
	usernameMap := make(map[string]GitLabUser)
	nameMap := make(map[string]GitLabUser)
	for _, user := range allUsers {
		if user.Email != "" {
			emailToUser[normalizeString(user.Email)] = user
		}
		if user.Username != "" {
			usernameMap[normalizeString(user.Username)] = user
		}
		if user.Name != "" {
			nameMap[normalizeString(user.Name)] = user
		}
	}

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		log.Printf("[ERROR] 获取项目列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个项目，开始获取 MR 数据...", len(projects))

	stats := MRStatistics{
		Authors:     []MRAuthor{},
		MergedByDay: []MergedByDay{},
	}

	authorCounts := make(map[string]int)
	authorUsernames := make(map[string]string) // name -> username (from API)
	mergedByDayMap := make(map[string]int)

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
				authorName := mr.Author.Name
				if authorName == "" {
					authorName = "Unknown"
				}
				authorCounts[authorName]++
				if mr.Author.Username != "" {
					authorUsernames[authorName] = mr.Author.Username
				}

				switch mr.State {
				case "merged":
					stats.Merged++
					if mr.MergedAt != nil {
						key := h.formatKey(*mr.MergedAt, period)
						mergedByDayMap[key]++
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

	// 构建作者列表并排序
	authorList := make([]MRAuthor, 0, len(authorCounts))
	for name, count := range authorCounts {
		apiUsername := authorUsernames[name]
		user, matched := h.matchUser("", name, apiUsername, emailToUser, nameMap, usernameMap)

		username := ""
		profileURL := ""
		if matched {
			username = user.Username
			profileURL = h.buildProfileURL(h.config.GitLabURL, user.Username)
		} else if apiUsername != "" {
			username = apiUsername
			profileURL = h.buildProfileURL(h.config.GitLabURL, apiUsername)
		}

		authorList = append(authorList, MRAuthor{
			Name:       name,
			Username:   username,
			ProfileURL: profileURL,
			Count:      count,
		})
	}
	sort.Slice(authorList, func(i, j int) bool {
		return authorList[i].Count > authorList[j].Count
	})
	if len(authorList) > 10 {
		authorList = authorList[:10]
	}
	stats.Authors = authorList

	// 构建按日合并列表
	mergedByDayList := make([]MergedByDay, 0, len(mergedByDayMap))
	for date, count := range mergedByDayMap {
		mergedByDayList = append(mergedByDayList, MergedByDay{Date: date, Count: count})
	}
	sort.Slice(mergedByDayList, func(i, j int) bool {
		return mergedByDayList[i].Date < mergedByDayList[j].Date
	})
	stats.MergedByDay = mergedByDayList

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (h *Handler) codeVolumeHandler(w http.ResponseWriter, r *http.Request) {
	_, days := h.parseQueryParams(r)
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	stats := CodeVolume{
		TopContributors: []TopContributor{},
		InactiveMembers: []InactiveMember{},
	}

	allUsers, err := h.gl.GetAllUsers()
	if err != nil {
		log.Printf("[ERROR] 获取用户列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取用户失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个用户", len(allUsers))

	// 构建多维度用户映射（大小写不敏感）
	emailToUser := make(map[string]GitLabUser)
	usernameToUser := make(map[string]GitLabUser)
	nameToUser := make(map[string]GitLabUser)
	for _, user := range allUsers {
		if user.Email != "" {
			normalizedEmail := strings.ToLower(strings.TrimSpace(user.Email))
			emailToUser[normalizedEmail] = user
		}
		if user.Username != "" {
			normalizedUsername := strings.ToLower(strings.TrimSpace(user.Username))
			usernameToUser[normalizedUsername] = user
		}
		if user.Name != "" {
			normalizedName := strings.ToLower(strings.TrimSpace(user.Name))
			nameToUser[normalizedName] = user
		}
	}
	log.Printf("[DEBUG] 用户映射统计: 邮箱 %d, 用户名 %d, 姓名 %d", len(emailToUser), len(usernameToUser), len(nameToUser))

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		log.Printf("[ERROR] 获取项目列表失败: %v", err)
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败: "+err.Error())
		return
	}

	log.Printf("[INFO] 找到 %d 个项目，开始获取代码量数据...", len(projects))

	// 临时存储贡献者统计
	contributorStats := make(map[string]ContributorStats)
	// 收集提交者邮箱映射：authorName -> authorEmail（同一个作者只保留第一个邮箱）
	authorToEmail := make(map[string]string)
	// 收集所有提交过的邮箱集合（用于判断零提交）
	commitEmails := make(map[string]bool)
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

				authorName := commit.AuthorName
				if authorName == "" {
					authorName = "Unknown"
				}

				// 收集邮箱映射
				if commit.AuthorEmail != "" {
					normalizedEmail := strings.ToLower(commit.AuthorEmail)
					commitEmails[normalizedEmail] = true
					if _, exists := authorToEmail[authorName]; !exists {
						authorToEmail[authorName] = normalizedEmail
					}
				}

				s := contributorStats[authorName]
				s.Additions += commit.Stats.Additions
				s.Deletions += commit.Stats.Deletions
				s.Commits++
				contributorStats[authorName] = s
			}
			mu.Unlock()
		}(project)
	}

	wg.Wait()

	if failedCount > 0 {
		log.Printf("[WARN] 完成代码量统计，成功 %d 个项目，失败 %d 个", len(projects)-failedCount, failedCount)
	}

	log.Printf("[DEBUG] GitLab 用户映射统计: 有邮箱 %d, 提交邮箱数 %d", len(emailToUser), len(commitEmails))

	// 构建已提交用户名集合（多维度匹配）
	commitUsers := make(map[int]bool) // user ID -> bool

	// 构建贡献者信息（带用户资料）
	contributorList := make([]TopContributor, 0, len(contributorStats))
	for authorName, authorStats := range contributorStats {
		authorEmail := authorToEmail[authorName]
		user, matched := h.matchUser(authorEmail, authorName, "", emailToUser, nameToUser, usernameToUser)

		username := ""
		profileURL := ""
		if matched {
			username = user.Username
			profileURL = h.buildProfileURL(h.config.GitLabURL, user.Username)
			commitUsers[user.ID] = true
		}

		contributorList = append(contributorList, TopContributor{
			Name:       authorName,
			Username:   username,
			ProfileURL: profileURL,
			Additions:  authorStats.Additions,
			Deletions:  authorStats.Deletions,
			Commits:    authorStats.Commits,
		})
	}

	// 零提交成员检测
	inactiveCount := 0
	for _, user := range allUsers {
		hasCommit := commitUsers[user.ID]

		// 备用检测：邮箱匹配
		if !hasCommit && user.Email != "" {
			if commitEmails[normalizeString(user.Email)] {
				hasCommit = true
			}
		}

		if !hasCommit {
			inactiveCount++
			profileURL := h.buildProfileURL(h.config.GitLabURL, user.Username)
			stats.InactiveMembers = append(stats.InactiveMembers, InactiveMember{
				Name:       user.Name,
				Username:   user.Username,
				ProfileURL: profileURL,
			})
			log.Printf("[DEBUG] 零提交成员: %s (@%s)", user.Name, user.Username)
		}
	}

	log.Printf("[INFO] 零提交成员数: %d / %d", inactiveCount, len(allUsers))
	sort.Slice(stats.InactiveMembers, func(i, j int) bool {
		return stats.InactiveMembers[i].Name < stats.InactiveMembers[j].Name
	})

	// 按提交数排序贡献者
	sort.Slice(contributorList, func(i, j int) bool {
		return contributorList[i].Commits > contributorList[j].Commits
	})

	// 只保留前 10 名
	if len(contributorList) > 10 {
		contributorList = contributorList[:10]
	}
	stats.TopContributors = contributorList

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
