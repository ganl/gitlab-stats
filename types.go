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
	"bytes"
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
	GitLabURL      string   `json:"gitlab_url"`
	Token          string   `json:"token"`
	Port           int      `json:"port"`
	MaxConcurrent  int      `json:"max_concurrent"`
	RequestTimeout Duration `json:"request_timeout"`
	CacheEnabled   bool     `json:"cache_enabled"`
	CacheTTL       Duration `json:"cache_ttl"`
	LogEnabled     bool     `json:"log_enabled"`
	LogRequests    bool     `json:"log_requests"`
	LogResponses   bool     `json:"log_responses"`

	// 定时统计与本地快照
	DataDir      string   `json:"data_dir"`
	StatsEnabled *bool    `json:"stats_enabled"`
	StatsCron    string   `json:"stats_cron"`
	StatsOnStart *bool    `json:"stats_on_start"`
	StatsTimeout Duration `json:"stats_timeout"`

	// ScanAllBranches 控制提交采集是否覆盖全部分支（GitLab commits 的 all=true）。
	//
	// 必须开启才能得到完整数据：本实例的默认分支普遍是 master/develop，
	// 而实际开发发生在 feature/* 等功能分支上，只扫默认分支会漏掉绝大部分提交。
	// 实测某项目默认分支 365 天 0 条，全分支 800 条；走 MR 流程的开发者
	// 其提交完全不在默认分支，会被误判为「零提交成员」。
	//
	// 代价是数据量成倍增长（实测 6.8x），因此必须配合下面的增量窗口使用。
	ScanAllBranches *bool `json:"scan_all_branches"`

	// RefreshOverlapDays 是每次增量采集向前回溯的天数。
	//
	// 增量采集会用新数据整体替换该区间内的旧记录，因此这个窗口需要覆盖：
	//   - 追溯到较早日期的补提交（amend / rebase / cherry-pick）
	//   - 创建较早、但近期才合并的 MR（合并状态会写回其创建日的记录）
	// 窗口越大越准确，代价是每次采集的分页数成比例上升。
	RefreshOverlapDays int `json:"refresh_overlap_days"`

	// BackfillChunkDays 是历史回填的单块跨度。
	//
	// 首次采集只回填最近一块，此后每次定时任务再向前推进一块，
	// 直到覆盖满 WindowDays。这样每次运行的成本有界——
	// 实测单个超大项目的一年全分支提交需 7 分钟以上，一次性回填不可接受。
	BackfillChunkDays int `json:"backfill_chunk_days"`

	// ExcludeAuthors 列出不计入贡献统计的作者标识（用户名或邮箱，大小写不敏感）。
	//
	// 用于排除 GitLab 未标记 bot 的自动化身份——例如打包脚本账号，
	// 其特征是每次构建只改动版本字符串，导致 additions 与 deletions 恒等。
	// 这类账号在 GitLab 侧 bot=false，无法靠 API 自动识别，只能由使用方显式声明。
	ExcludeAuthors []string `json:"exclude_authors"`

	// ExemptFromInactive 列出豁免「未参与成员」检测的账号标识
	// （用户名 / 邮箱 / 姓名，大小写不敏感）。
	//
	// 与 ExcludeAuthors 的关键区别是作用范围：
	//   - ExcludeAuthors 把账号从**全部统计**中剔除（提交、代码量、排行都不计）；
	//   - ExemptFromInactive 只把账号从**未参与成员名单**中取下，
	//     其提交与 MR 仍照常计入各项统计。
	//
	// 适用对象是「本就不承担日常编码指标」的成员：管理层 / 领导、
	// 挂名在项目里但不参与开发的岗位。他们若偶有提交，在较短展示范围
	// （如最近 7 天、最近 30 天）下会因窗口内没有记录而被列为未参与，
	// 用这个名单可直接豁免，同时不损失其真实贡献数据。
	ExemptFromInactive []string `json:"exempt_from_inactive"`
}

// ExcludeAuthorSet 返回排除名单的规范化集合，便于 O(1) 判定。
func (c *Config) ExcludeAuthorSet() map[string]bool {
	if len(c.ExcludeAuthors) == 0 {
		return nil
	}
	set := make(map[string]bool, len(c.ExcludeAuthors)*2)
	for _, a := range c.ExcludeAuthors {
		// 先规范化再判空：纯空白项规范化后为空串，不应进入集合。
		if n := normalizeString(a); n != "" {
			set[n] = true
		}
	}
	return set
}

// StatsEnabledOrDefault 默认开启定时统计。
func (c *Config) StatsEnabledOrDefault() bool {
	return c.StatsEnabled == nil || *c.StatsEnabled
}

// StatsOnStartOrDefault 默认启动时立即统计一次。
func (c *Config) StatsOnStartOrDefault() bool {
	return c.StatsOnStart == nil || *c.StatsOnStart
}

// StatsTimeoutOr 返回统计任务超时时间，未配置时为默认值。
func (c *Config) StatsTimeoutOr(defaultTimeout time.Duration) time.Duration {
	if c.StatsTimeout <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.StatsTimeout)
}

// ScanAllBranchesOrDefault 默认开启全分支扫描。
// 关闭会退回「只扫默认分支」，在本实例上会漏掉功能分支上的绝大部分提交。
func (c *Config) ScanAllBranchesOrDefault() bool {
	return c.ScanAllBranches == nil || *c.ScanAllBranches
}

// RefreshOverlapDaysOr 返回增量刷新回溯天数，未配置或非正数时用默认值。
func (c *Config) RefreshOverlapDaysOr(def int) int {
	if c.RefreshOverlapDays <= 0 {
		return def
	}
	return c.RefreshOverlapDays
}

// BackfillChunkDaysOr 返回历史回填的单块跨度，未配置或非正数时用默认值。
func (c *Config) BackfillChunkDaysOr(def int) int {
	if c.BackfillChunkDays <= 0 {
		return def
	}
	return c.BackfillChunkDays
}

// Commit 是 GitLab 提交对象。
//
// stats 在部分提交上不会被 GitLab 返回（如合并提交、超大提交，
// 或未开启 with_stats），因此不能把它反序列化成普通 int 结构体——
// 否则「接口没给行数」会被静默当成「0 行变更」，导致代码量统计偏低。
// 这里保存原始 JSON，由 HasStats()/LineStats() 显式表达三态语义：
// 字段缺失、字段为 null、字段存在但为 0。
type Commit struct {
	ID          string
	CreatedAt   time.Time
	AuthorName  string
	AuthorEmail string

	statsRaw json.RawMessage
}

// HasStats 表示 GitLab 确实返回了 stats 字段。
//
// 只要有 stats（哪怕 additions/deletions 都是 0），也算已提供；
// 只有字段缺失或为 null 时才返回 false。
func (c Commit) HasStats() bool {
	trimmed := bytes.TrimSpace(c.statsRaw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// LineStats 返回变更行数。第二个返回值表示数据是否可信：
// GitLab 未提供 stats 时返回 (0, 0, false)，调用方应据此标记统计不完整。
func (c Commit) LineStats() (additions, deletions int, ok bool) {
	if !c.HasStats() {
		return 0, 0, false
	}

	var s struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	}
	if err := json.Unmarshal(c.statsRaw, &s); err != nil {
		return 0, 0, false
	}
	return s.Additions, s.Deletions, true
}

// Stats 保留旧访问方式，返回变更行数（不做可信性判断）。
func (c Commit) Stats() (additions, deletions int) {
	additions, deletions, _ = c.LineStats()
	return
}

func (c *Commit) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string          `json:"id"`
		CreatedAt   time.Time       `json:"created_at"`
		AuthorName  string          `json:"author_name"`
		AuthorEmail string          `json:"author_email"`
		Stats       json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	c.ID = raw.ID
	c.CreatedAt = raw.CreatedAt
	c.AuthorName = raw.AuthorName
	c.AuthorEmail = raw.AuthorEmail
	c.statsRaw = raw.Stats
	return nil
}

// MarshalJSON 与 UnmarshalJSON 对称，保证 Commit 可以安全地序列化再反序列化
// （测试替身、日志、缓存都需要这一点）。
func (c Commit) MarshalJSON() ([]byte, error) {
	payload := struct {
		ID          string          `json:"id"`
		CreatedAt   time.Time       `json:"created_at"`
		AuthorName  string          `json:"author_name"`
		AuthorEmail string          `json:"author_email"`
		Stats       json.RawMessage `json:"stats,omitempty"`
	}{
		ID:          c.ID,
		CreatedAt:   c.CreatedAt,
		AuthorName:  c.AuthorName,
		AuthorEmail: c.AuthorEmail,
		Stats:       c.statsRaw,
	}
	return json.Marshal(payload)
}

// NewCommitWithStats 构造一个带变更行数的提交。主要用于测试替身与内部构造，
// 生产代码的提交对象一律经 UnmarshalJSON 从 GitLab 响应解析。
func NewCommitWithStats(id string, createdAt time.Time, authorName, authorEmail string, additions, deletions int) Commit {
	raw, _ := json.Marshal(struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	}{Additions: additions, Deletions: deletions})

	return Commit{
		ID:          id,
		CreatedAt:   createdAt,
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		statsRaw:    raw,
	}
}

// NewCommitWithoutStats 构造一个 GitLab 未提供变更行数的提交（如合并提交）。
func NewCommitWithoutStats(id string, createdAt time.Time, authorName, authorEmail string) Commit {
	return Commit{
		ID:          id,
		CreatedAt:   createdAt,
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		statsRaw:    nil,
	}
}

// MergeRequest 是 GitLab 合并请求对象。
type MergeRequest struct {
	ID        int        `json:"id"`
	State     string     `json:"state"`
	MergedAt  *time.Time `json:"merged_at"`
	CreatedAt time.Time  `json:"created_at"`
	Author    struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Username string `json:"username"`
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
	Email    string `json:"email"`

	// Bot 由 GitLab 标记（group bot / project bot / 服务账号）。
	// 这类账号不参与「零提交成员」考核，否则报表会混入自动化身份。
	Bot bool `json:"bot"`
}

type CommitFrequency struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type MRStatistics struct {
	Total       int           `json:"total"`
	Merged      int           `json:"merged"`
	Opened      int           `json:"opened"`
	Closed      int           `json:"closed"`
	Authors     []MRAuthor    `json:"authors"`
	MergedByDay []MergedByDay `json:"merged_by_day"`
}

type MRAuthor struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	ProfileURL string `json:"profile_url"`
	Count      int    `json:"count"`
}

type MergedByDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type CodeVolume struct {
	TotalAdditions  int              `json:"total_additions"`
	TotalDeletions  int              `json:"total_deletions"`
	TotalCommits    int              `json:"total_commits"`
	TopContributors []TopContributor `json:"top_contributors"`
	InactiveMembers []InactiveMember `json:"inactive_members"`
}

type TopContributor struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	ProfileURL string `json:"profile_url"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	Commits    int    `json:"commits"`
}

type InactiveMember struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	ProfileURL string `json:"profile_url"`
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
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.StatsCron == "" {
		c.StatsCron = DefaultStatsCron
	}
	if c.StatsTimeout == 0 {
		c.StatsTimeout = Duration(DefaultStatsTimeout)
	}
}
