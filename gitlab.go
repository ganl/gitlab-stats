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
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultPerPage = 100

type GitLabClient struct {
	baseURL      string
	token        string
	client       *http.Client
	cache        *Cache
	logEnabled   bool
	logRequests  bool
	logResponses bool
}

func NewGitLabClient(cfg *Config, cache *Cache) *GitLabClient {
	return &GitLabClient{
		baseURL: strings.TrimSuffix(cfg.GitLabURL, "/") + "/api/v4",
		token:   cfg.Token,
		client: &http.Client{
			Timeout: time.Duration(cfg.RequestTimeout),
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		cache:        cache,
		logEnabled:   cfg.LogEnabled,
		logRequests:  cfg.LogRequests,
		logResponses: cfg.LogResponses,
	}
}

func (gl *GitLabClient) request(endpoint string, params map[string]string) ([]byte, error) {
	cacheKey := "req:" + endpoint + fmt.Sprintf("%v", params)
	if cached, found := gl.cache.Get(cacheKey); found {
		if gl.logEnabled && gl.logRequests {
			log.Printf("[CACHE HIT] %s %s", "GET", endpoint)
		}
		return cached.([]byte), nil
	}

	reqURL := gl.baseURL + endpoint
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Add(k, v)
		}
		reqURL += "?" + q.Encode()
	}

	if gl.logEnabled && gl.logRequests {
		maskToken := func(u string) string {
			if len(u) > 20 {
				return u[:20] + "***"
			}
			return u
		}
		log.Printf("[REQUEST] GET %s (URL: %s, params: %v, token: %s)", endpoint, reqURL, params, maskToken(gl.token))
	}

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("PRIVATE-TOKEN", gl.token)

	resp, err := gl.client.Do(req)
	if err != nil {
		if gl.logEnabled {
			log.Printf("[ERROR] Request failed: %v", err)
		}
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if gl.logEnabled {
			log.Printf("[ERROR] Read response failed: %v", err)
		}
		return nil, err
	}

	if gl.logEnabled && gl.logResponses {
		respBody := string(body)
		if len(respBody) > 500 {
			respBody = respBody[:500] + "...(truncated)"
		}
		log.Printf("[RESPONSE] %s - Status: %d, Body: %s", endpoint, resp.StatusCode, respBody)
	}

	if resp.StatusCode != http.StatusOK {
		if gl.logEnabled {
			log.Printf("[ERROR] GitLab API error: %d - URL: %s - %s", resp.StatusCode, reqURL, string(body))
		}
		return nil, fmt.Errorf("GitLab API 错误: %d - %s", resp.StatusCode, string(body))
	}

	gl.cache.Set(cacheKey, body)
	return body, nil
}

func (gl *GitLabClient) fetchAll(endpoint string, params map[string]string, parser func([]byte) (int, error)) error {
	page := 1

	for {
		reqParams := make(map[string]string)
		for k, v := range params {
			reqParams[k] = v
		}
		reqParams["page"] = fmt.Sprintf("%d", page)
		reqParams["per_page"] = fmt.Sprintf("%d", defaultPerPage)

		data, err := gl.request(endpoint, reqParams)
		if err != nil {
			return err
		}

		count, err := parser(data)
		if err != nil {
			return err
		}

		if count == 0 {
			break
		}
		if count < defaultPerPage {
			break
		}
		page++
	}
	return nil
}

func (gl *GitLabClient) GetAllProjects() ([]Project, error) {
	var allProjects []Project

	err := gl.fetchAll("/projects", map[string]string{
		"order_by": "updated_at",
		"sort":     "desc",
	}, func(data []byte) (int, error) {
		var projects []Project
		if err := json.Unmarshal(data, &projects); err != nil {
			return 0, err
		}
		allProjects = append(allProjects, projects...)
		return len(projects), nil
	})

	return allProjects, err
}

// GetAllUsers 拉取参与统计的成员名单。
//
// 刻意只取 active=true 的账号：实例上多数账号处于 blocked / deactivated / banned
// （已离职、测试账号、长期停用），实测这些账号占比超过六成。
// 把停用账号拉进来会带来两个问题：
//   - 它们不会出现在「未参与成员」名单里——离职的人不该被当作
//     「在职却未参与开发」点名；
//   - 用户表体积与匹配开销成倍上升，而收益只是给已离职者的历史提交署上真名。
//
// 若改动这里的过滤条件，务必同步确认 aggregate.go 中未参与成员检测的口径。
func (gl *GitLabClient) GetAllUsers() ([]GitLabUser, error) {
	var allUsers []GitLabUser

	err := gl.fetchAll("/users", map[string]string{
		"active": "true",
	}, func(data []byte) (int, error) {
		var users []GitLabUser
		if err := json.Unmarshal(data, &users); err != nil {
			return 0, err
		}
		allUsers = append(allUsers, users...)
		return len(users), nil
	})

	return allUsers, err
}

// GetCommits 拉取项目在 [since, until] 区间内的提交。
//
// allBranches 为 true 时附带 all=true，返回所有分支上的提交。
// 这是拿到完整数据的必要条件：本实例的默认分支普遍是 master/develop，
// 实际开发发生在 feature/* 等功能分支上，只看默认分支会漏掉绝大多数提交
// （实测某项目默认分支 365 天 0 条，全分支 800 条）。
//
// GitLab 会按 commit id 去重，多分支共有的提交只返回一次，无需调用方去重。
func (gl *GitLabClient) GetCommits(projectID int, since, until time.Time, allBranches bool) ([]Commit, error) {
	var allCommits []Commit

	params := map[string]string{
		"since":      since.Format(time.RFC3339),
		"until":      until.Format(time.RFC3339),
		"with_stats": "true",
	}
	if allBranches {
		params["all"] = "true"
	}

	err := gl.fetchAll(fmt.Sprintf("/projects/%d/repository/commits", projectID), params, func(data []byte) (int, error) {
		var commits []Commit
		if err := json.Unmarshal(data, &commits); err != nil {
			return 0, err
		}
		allCommits = append(allCommits, commits...)
		return len(commits), nil
	})

	return allCommits, err
}

// GetUserByUsername 按用户名查用户详情，用于补齐 MR 作者缺失的邮箱信息。
func (gl *GitLabClient) GetUserByUsername(username string) (GitLabUser, error) {
	data, err := gl.request("/users", map[string]string{"username": username})
	if err != nil {
		return GitLabUser{}, err
	}

	var users []GitLabUser
	if err := json.Unmarshal(data, &users); err != nil {
		return GitLabUser{}, err
	}
	for _, u := range users {
		// GitLab 的 username 查询是模糊匹配，这里只认精确相等。
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return GitLabUser{}, fmt.Errorf("未找到用户: %s", username)
}

func (gl *GitLabClient) GetMergeRequests(projectID int, since, until time.Time) ([]MergeRequest, error) {
	var allMRs []MergeRequest

	err := gl.fetchAll(fmt.Sprintf("/projects/%d/merge_requests", projectID), map[string]string{
		"created_after":  since.Format(time.RFC3339),
		"created_before": until.Format(time.RFC3339),
		"state":          "all",
	}, func(data []byte) (int, error) {
		var mrs []MergeRequest
		if err := json.Unmarshal(data, &mrs); err != nil {
			return 0, err
		}
		allMRs = append(allMRs, mrs...)
		return len(mrs), nil
	})

	return allMRs, err
}
