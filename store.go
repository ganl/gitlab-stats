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
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// WindowDays 是所有统计口径的固定窗口，与 /api/stats 查询参数 days 解耦。
// 前端按展示范围裁剪，因此一次落盘可服务全部展示范围。
const WindowDays = 365

// snapshotSchemaVersion 用于识别快照格式。升级聚合口径时递增该值，
// 服务启动发现旧版本快照会直接忽略并重新采集。
//
// v2: author_key 改为始终输出（去掉 omitempty），使「无匹配身份」可被区分。
//
// v3: 用户匹配移除姓名维度（只认 email / username）；提交采集改为全分支；
// 快照新增 CoveredFrom 覆盖区间以支持增量采集。
//
// v4: 归日统一到固定时区（东八区），不再使用提交自带的时区偏移；
// 采集区间边界对齐到自然日，covered_from 改为「区间集合」语义。
// 前三个版本按提交自带时区归日，且区间边界可能落在一天中间，
// 与 v4 的日期口径不可混用，必须整体丢弃重采。
const snapshotSchemaVersion = 4

// Snapshot 是一次完整统计的落盘结果。所有展示范围共用这一份基准数据。
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	DurationMS    int64     `json:"duration_ms"`
	WindowDays    int       `json:"window_days"`
	Scope         string    `json:"scope"`

	// CoveredFrom 是快照已完整覆盖的最早日期（YYYY-MM-DD，含当天）。
	//
	// 增量采集据此判断历史是否已回填到窗口起点：
	//   - CoveredFrom <= 窗口起点：历史已补全，此后每次只刷新最近区间。
	//   - CoveredFrom > 窗口起点：仍在回填中，下次运行再向前推进一块。
	// 为空表示尚无可用历史（首次采集前的状态）。
	CoveredFrom string `json:"covered_from,omitempty"`

	// ScanAllBranches 记录本次采集是否覆盖了全部分支。
	// 随快照落盘是为了让口径可追溯：只扫默认分支与全分支的结果不可比。
	ScanAllBranches bool `json:"scan_all_branches,omitempty"`

	// ExcludedAuthors 记录本次采集生效的排除名单（规范化后的用户名/邮箱）。
	// 随快照落盘是为了让口径可追溯：同一个数字在统计口径变化前后不应被混为一谈。
	ExcludedAuthors []string `json:"excluded_authors,omitempty"`

	// ExemptFromInactive 记录本次采集生效的「未参与成员」豁免名单。
	//
	// 同样随快照落盘以保证口径可追溯，但它只影响未参与名单的构成，
	// 不改变任何统计数据。为空是合法状态（旧版本快照即如此），
	// 因此该字段缺省时等价于「无豁免」，不需要为此升级 schema 触发整体重采。
	ExemptFromInactive []string `json:"exempt_from_inactive,omitempty"`

	// 按天聚合，保证任意侧字段齐全，缺失日期补 0。
	Daily  []DailyStat  `json:"daily"`
	Totals SnapshotMeta `json:"totals"`
}

// CoverageComplete 表示历史是否已回填到指定窗口起点。
func (s *Snapshot) CoverageComplete(windowStart string) bool {
	if s == nil || s.CoveredFrom == "" {
		return false
	}
	return s.CoveredFrom <= windowStart
}

// DateRange 是一个日期粒度的采集区间（两端均含）。
//
// 用区间集合而非单个起点描述「本次采集覆盖了哪些日期」，是因为增量采集天然是
// 不连续的：一次运行会采「最近的刷新段」+「更早的一块回填段」，两段之间隔着
// 上次已经采过、本次不需要重采的历史。若只用一个最早起点概括，中间那段历史
// 会被误判为「已重采」而丢弃，形成空洞。
type DateRange struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// Covers 判断某日期是否落在本区间内。
func (r DateRange) Covers(date string) bool {
	return date >= r.Since && date <= r.Until
}

// CoversDate 判断日期是否落在任一区间内。
func CoversDate(date string, ranges []DateRange) bool {
	for _, r := range ranges {
		if r.Covers(date) {
			return true
		}
	}
	return false
}

// MergeIncremental 把新采集的分片合并进上一份快照。
//
// 合并规则：日报记录以「日期」为最小替换单位——
//   - 落在**任一**本次采集区间内的旧记录整体丢弃，由本次采集结果取代（避免重复累加）；
//   - 不在任何本次区间内的旧记录保留（本次没有重新采集它们）；
//   - 超出 [windowStart, now] 的旧记录丢弃（滑出统计窗口）。
//
// 之所以按日期整体替换而非逐字段相加：DailyStat 把提交与 MR 计数合并在同一条记录里，
// 二者由同一次采集同时产出，按日期整体替换才能保证两个维度始终来自同一批次数据。
func MergeIncremental(old, fresh *Snapshot, covered []DateRange, windowStart string) *Snapshot {
	if old == nil || old.Empty() || old.SchemaMismatch() {
		return fresh
	}

	merged := make([]DailyStat, 0, len(old.Daily)+len(fresh.Daily))
	for i := range old.Daily {
		d := &old.Daily[i]
		if CoversDate(d.Date, covered) {
			continue // 本次已重新采集，让位给新数据
		}
		if d.Date < windowStart {
			continue // 已滑出统计窗口
		}
		merged = append(merged, *d)
	}
	merged = append(merged, fresh.Daily...)

	// 用稳定排序保证同一日期内的记录顺序可复现，便于快照 diff 比对。
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Date != merged[j].Date {
			return merged[i].Date < merged[j].Date
		}
		return merged[i].AuthorKey < merged[j].AuthorKey
	})

	out := *fresh
	out.Daily = merged

	// 覆盖起点取两者更早者：本次采集可能只是刷新最近区间，
	// 历史覆盖范围应由上一份快照延续下来。
	out.CoveredFrom = fresh.CoveredFrom
	if old.CoveredFrom != "" && (out.CoveredFrom == "" || old.CoveredFrom < out.CoveredFrom) {
		out.CoveredFrom = old.CoveredFrom
	}
	// 但不能早于窗口起点：滑出窗口的旧记录已被裁掉，若仍宣称覆盖会误导回填判断。
	if out.CoveredFrom != "" && out.CoveredFrom < windowStart {
		out.CoveredFrom = windowStart
	}
	return &out
}

// DailyStat 是按人 × 按天的最小不可再分事实。
//
// 它同时支撑提交频率、代码量、贡献排行、零提交检测和 MR 趋势：
//   - Commits 用于提交频率与活跃度判定
//   - Additions / Deletions 用于代码量与贡献排行
//   - CreatedMRs / MergedMRs / OpenedMRs / ClosedMRs 用于 MR 状态统计
//   - MergedAt 记录合并时间，使 MR 合并趋势可以按合并日而非创建日落桶
//
// 因此一次采集即可覆盖全部面板，无需为每个指标重复拉取。
type DailyStat struct {
	Date string `json:"date"`
	// AuthorKey 是 GitLab 用户 ID，未匹配到时为 0。
	// 不使用 omitempty：0 在这里是有意义的取值（上游开源提交者等），
	// 显式输出才能让读取方区分「无匹配身份」与字段缺失。
	AuthorKey       int    `json:"author_key"`
	Name            string `json:"name"`
	Email           string `json:"email,omitempty"`
	Username        string `json:"username,omitempty"`
	ProfileURL      string `json:"profile_url,omitempty"`
	Commits         int    `json:"commits"`
	Additions       int    `json:"additions"`
	Deletions       int    `json:"deletions"`
	CreatedMRs      int    `json:"created_mrs,omitempty"`
	MergedMRs       int    `json:"merged_mrs,omitempty"`
	OpenedMRs       int    `json:"opened_mrs,omitempty"`
	ClosedMRs       int    `json:"closed_mrs,omitempty"`
	MergedAt        string `json:"merged_at,omitempty"`
	StatsIncomplete bool   `json:"stats_incomplete,omitempty"`
}

// HasActivity 判断该日记录是否代表一次实际参与。
//
// 提交与 MR 任一存在即为参与：本实例大量开发者走 MR 流程，
// 其代码提交可能不出现在默认分支的提交列表里，只看 Commits 会把人误判为未参与。
func (d *DailyStat) HasActivity() bool {
	return d.Commits > 0 || d.CreatedMRs > 0 || d.MergedMRs > 0 ||
		d.OpenedMRs > 0 || d.ClosedMRs > 0
}

// SnapshotMeta 保存与时间窗口无关的全局口径（人 / 项目维度）。
type SnapshotMeta struct {
	ProjectsScanned int            `json:"projects_scanned"`
	UsersScanned    int            `json:"users_scanned"`
	ProjectErrors   []string       `json:"project_errors,omitempty"`
	Users           []SnapshotUser `json:"users,omitempty"`
}

// SnapshotUser 保证零提交成员检测不依赖时间窗口内的提交记录。
// 只有确实提交过的用户会出现在 Daily 中；从未提交的用户这里保留记录。
//
// Bot 为 true 的账号（group bot / project bot / 服务账号）在零提交判定中会被跳过，
// 否则报表会把自动化身份当成「未参与开发的成员」。
type SnapshotUser struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Email      string `json:"email,omitempty"`
	ProfileURL string `json:"profile_url"`
	Bot        bool   `json:"bot,omitempty"`
}

// Store 负责快照的原子读写。读操作走内存，避免每次请求解析 JSON。
type Store struct {
	mu       sync.RWMutex
	dir      string
	filePath string
	current  *Snapshot
	loaded   bool
}

func NewStore(dir string) *Store {
	return &Store{
		dir:      dir,
		filePath: filepath.Join(dir, "stats.json"),
	}
}

func (s *Store) Dir() string { return s.dir }

// Save 先写临时文件再 rename，避免读到半截 JSON。
func (s *Store) Save(snap *Snapshot) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化快照失败: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, "stats-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	// Windows 上 rename 目标已存在会失败，先移除旧文件。
	if err := os.Rename(tmpName, s.filePath); err != nil {
		os.Remove(s.filePath)
		if err := os.Rename(tmpName, s.filePath); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("替换快照文件失败: %w", err)
		}
	}

	s.mu.Lock()
	s.current = snap
	s.loaded = true
	s.mu.Unlock()

	return nil
}

// Load 从磁盘读取快照。文件不存在不算错误，返回 (nil, false, nil)。
func (s *Store) Load() (*Snapshot, bool, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读取快照失败: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false, fmt.Errorf("解析快照失败: %w", err)
	}

	s.mu.Lock()
	s.current = &snap
	s.loaded = true
	s.mu.Unlock()

	return &snap, true, nil
}

// Current 返回内存中的快照指针（只读使用，调用方不得修改）。
func (s *Store) Current() (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.loaded
}

// Reload 丢弃内存副本并重新从磁盘加载。
func (s *Store) Reload() (*Snapshot, bool, error) {
	s.mu.Lock()
	s.current = nil
	s.loaded = false
	s.mu.Unlock()
	return s.Load()
}

// Empty 判断快照是否尚无有效数据。
func (s *Snapshot) Empty() bool {
	return s == nil || len(s.Daily) == 0
}

// identityMatchesAny 判断一组身份标识是否命中名单中的任意一项。
//
// 判定同时匹配用户名、邮箱与姓名，且两侧都做大小写与空白规范化——
// 配置侧可能已被规范化，但名单也可能来自反序列化或直接构造，
// 因此这里不假设已规范化，两侧同时归一才稳妥。
//
// 「排除」与「豁免」是两份语义不同的名单，但匹配规则必须完全一致，
// 否则同一个账号在两条路径上会得到不同结论。故抽成唯一实现。
func identityMatchesAny(list []string, username, email, name string) bool {
	if len(list) == 0 {
		return false
	}
	username = normalizeString(username)
	email = normalizeString(email)
	name = normalizeString(name)

	for _, raw := range list {
		ex := normalizeString(raw)
		if ex == "" {
			continue
		}
		if username != "" && username == ex {
			return true
		}
		if email != "" && email == ex {
			return true
		}
		if name != "" && name == ex {
			return true
		}
	}
	return false
}

// IsIdentityExcluded 判断一组身份标识是否命中排除名单。
//
// 这是排除逻辑的唯一实现：日报记录与用户表都走这里，避免两条路径口径漂移
// ——曾出现过记录被排除、但用户表未同步排除，导致自动化账号反而落进
// 「未参与成员」名单的情况。
func (s *Snapshot) IsIdentityExcluded(username, email, name string) bool {
	if s == nil {
		return false
	}
	return identityMatchesAny(s.ExcludedAuthors, username, email, name)
}

// IsIdentityExemptFromInactive 判断一组身份标识是否命中未参与豁免名单。
func (s *Snapshot) IsIdentityExemptFromInactive(username, email, name string) bool {
	if s == nil {
		return false
	}
	return identityMatchesAny(s.ExemptFromInactive, username, email, name)
}

// IsUserExemptFromInactive 判断用户表中的成员是否豁免未参与检测。
//
// 命中者不出现在「未参与成员」名单里，但其历史提交与 MR 照常计入统计
// ——这是与 IsUserExcluded 的本质区别（后者会把统计数据一并剔除）。
func (s *Snapshot) IsUserExemptFromInactive(u *SnapshotUser) bool {
	if u == nil {
		return false
	}
	return s.IsIdentityExemptFromInactive(u.Username, u.Email, u.Name)
}

// IsAuthorExcluded 判断某条日记录是否属于被排除的自动化身份。
//
// 被排除的记录在聚合阶段直接跳过，不进入提交频率、代码量、贡献排行和 MR 统计。
func (s *Snapshot) IsAuthorExcluded(d *DailyStat) bool {
	if d == nil {
		return false
	}
	return s.IsIdentityExcluded(d.Username, d.Email, d.Name)
}

// IsUserExcluded 判断用户表中的成员是否属于被排除的自动化身份。
//
// 用户表没有日报记录，若不单独判定，被排除的账号会因「没有任何活跃标记」
// 而被判定为未参与，与排除意图完全相反。
func (s *Snapshot) IsUserExcluded(u *SnapshotUser) bool {
	if u == nil {
		return false
	}
	return s.IsIdentityExcluded(u.Username, u.Email, u.Name)
}

// StaleThreshold 是快照的保鲜期。超过该时长前端会给出「数据可能过期」提示。
const StaleThreshold = 26 * time.Hour

// Stale 表示快照是否已明显过期（超过一天未更新，通常意味着定时任务连续失败）。
func (s *Snapshot) Stale() bool {
	if s == nil || s.GeneratedAt.IsZero() {
		return true
	}
	return time.Since(s.GeneratedAt) > StaleThreshold
}

// SchemaMismatch 表示磁盘上的快照由旧版本代码生成，聚合口径可能不一致，应当丢弃。
func (s *Snapshot) SchemaMismatch() bool {
	return s == nil || s.SchemaVersion != snapshotSchemaVersion
}
