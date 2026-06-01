package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"
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
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败")
		return
	}

	commitByPeriod := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			commits, err := h.gl.GetCommits(p.ID, startDate, endDate)
			if err != nil {
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

	var result []CommitFrequency
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
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败")
		return
	}

	stats := MRStatistics{
		Authors:     make(map[string]int),
		MergedByDay: make(map[string]int),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			mrs, err := h.gl.GetMergeRequests(p.ID, startDate, endDate)
			if err != nil {
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
		h.jsonError(w, http.StatusInternalServerError, "获取用户失败")
		return
	}

	userNames := make(map[string]bool)
	for _, user := range allUsers {
		userNames[user.Name] = true
	}

	projects, err := h.gl.GetAllProjects()
	if err != nil {
		h.jsonError(w, http.StatusInternalServerError, "获取项目失败")
		return
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, h.config.MaxConcurrent)

	for _, project := range projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			commits, err := h.gl.GetCommits(p.ID, startDate, endDate)
			if err != nil {
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

	for userName := range userNames {
		if _, hasCommit := stats.TopContributors[userName]; !hasCommit {
			stats.InactiveMembers = append(stats.InactiveMembers, userName)
		}
	}
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
