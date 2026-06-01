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
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		var n int64
		if err := json.Unmarshal(data, &n); err != nil {
			return err
		}
		*d = Duration(n)
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

type Config struct {
	GitLabURL       string   `json:"gitlab_url"`
	Token           string   `json:"token"`
	Port            int      `json:"port"`
	MaxConcurrent   int      `json:"max_concurrent"`
	RequestTimeout  Duration `json:"request_timeout"`
	CacheEnabled    bool     `json:"cache_enabled"`
	CacheTTL        Duration `json:"cache_ttl"`
}

type Commit struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	AuthorName string    `json:"author_name"`
	Stats      struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"stats"`
}

type MergeRequest struct {
	ID        int        `json:"id"`
	State     string     `json:"state"`
	MergedAt  *time.Time `json:"merged_at"`
	CreatedAt time.Time  `json:"created_at"`
	Author    struct {
		Name string `json:"name"`
	} `json:"author"`
}

type Project struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type GitLabUser struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type CommitFrequency struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type MRStatistics struct {
	Total       int                  `json:"total"`
	Merged      int                  `json:"merged"`
	Opened      int                  `json:"opened"`
	Closed      int                  `json:"closed"`
	Authors     map[string]int       `json:"authors"`
	MergedByDay map[string]int       `json:"merged_by_day"`
}

type CodeVolume struct {
	TotalAdditions   int                      `json:"total_additions"`
	TotalDeletions   int                      `json:"total_deletions"`
	TotalCommits     int                      `json:"total_commits"`
	TopContributors map[string]ContributorStats `json:"top_contributors"`
	InactiveMembers []string                `json:"inactive_members"`
}

type ContributorStats struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Commits   int `json:"commits"`
}

func (c *Config) SetDefaults() {
	if c.Port == 0 {
		c.Port = 8080
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 20
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = Duration(30 * time.Second)
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = Duration(5 * time.Minute)
	}
}
