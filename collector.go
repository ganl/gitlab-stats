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
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSnapshotEmpty 表示统计窗口内没有采集到任何数据。
var ErrSnapshotEmpty = errors.New("统计窗口内没有任何提交或 MR 数据")

// Collector 把 GitLab 原始数据采集并聚合成一份快照。
//
// 设计要点：
//   - 全程只发 GET 只读请求，不对 GitLab 做任何写入。
//   - 一次采集覆盖全部统计口径，避免同一批 commits 被重复拉取。
//   - 单项目失败不中断整体统计，失败项记入 ProjectErrors 供前端提示。
type Collector struct {
	gl  *GitLabClient
	cfg *Config
}

func NewCollector(gl *GitLabClient, cfg *Config) *Collector {
	return &Collector{gl: gl, cfg: cfg}
}

// 增量采集的默认参数。
const (
	// DefaultRefreshOverlapDays 是每次刷新向前回溯的天数。
	// 该窗口内会被整体重采，需覆盖追溯推送的提交与近期才合并的 MR。
	DefaultRefreshOverlapDays = 30

	// DefaultBackfillChunkDays 是历史回填的单块跨度。
	// 首次采集只取最近一块，之后每次运行再向前推进一块，使单次成本有界。
	DefaultBackfillChunkDays = 30
)

// CollectRange 是一段需要采集的日期区间（闭区间）。
type CollectRange struct {
	Since time.Time
	Until time.Time
}

// Collect 执行一次采集，并把结果合并进上一份快照。
//
// prev 为上一份可用快照；为 nil、为空或口径不匹配时按首次采集处理。
//
// 采集区间是增量式的，每次只覆盖「最近一段」加「历史回填的一块」：
//   - 刷新段：最近 RefreshOverlapDays 天，整体重采以吸收追溯推送与晚合并的 MR。
//   - 回填段：若历史尚未补满 WindowDays，再向前推进 BackfillChunkDays 天。
//
// 两段互不重叠，且都远小于统计窗口，因此单次运行的请求量有界——
// 实测单个超大项目一次拉取一年全分支提交需 7 分钟以上，必须在时间维度上切片。
func (c *Collector) Collect(ctx context.Context, prev *Snapshot, onProgress func(current, total int)) (*Snapshot, error) {
	started := time.Now()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	users, err := c.gl.GetAllUsers()
	if err != nil {
		return nil, fmt.Errorf("获取用户列表失败: %w", err)
	}

	projects, err := c.gl.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("获取项目列表失败: %w", err)
	}

	if onProgress != nil {
		onProgress(0, len(projects))
	}

	// 用户匹配只认「邮箱」与「用户名」两个稳定维度。
	// 姓名不具备规律（同一人可能写全名/简称/英文名），
	// 实测按姓名匹配会把不同机器的 root 账号并成一人，故不再建立姓名索引。
	byEmail, byUsername := indexUsers(users)

	ranges := c.planRanges(prev, started)

	acc := &accumulator{
		cfg:    c.cfg,
		daily:  make(map[string]map[string]*DailyStat),
		lookup: make(map[string]GitLabUser),
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, c.cfg.MaxConcurrent)
	var completed int64
	var failedMu sync.Mutex
	var failedProjects []string

	for _, project := range projects {
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(p Project) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			err := c.collectProject(ctx, p, ranges, byEmail, byUsername, acc)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[WARN] 项目 [%s] (ID: %d) 采集失败: %v", p.Name, p.ID, err)
				failedMu.Lock()
				failedProjects = append(failedProjects, fmt.Sprintf("%s: %v", p.Name, err))
				failedMu.Unlock()
			}

			n := atomic.AddInt64(&completed, 1)
			if onProgress != nil {
				onProgress(int(n), len(projects))
			}
		}(project)
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 用户表快照，零提交检测不依赖时间窗口内的提交记录。
	snapshotUsers := make([]SnapshotUser, 0, len(users))
	for _, u := range users {
		snapshotUsers = append(snapshotUsers, SnapshotUser{
			ID:         u.ID,
			Name:       u.Name,
			Username:   u.Username,
			Email:      u.Email,
			ProfileURL: buildProfileURL(c.cfg.GitLabURL, u.Username),
			Bot:        u.Bot,
		})
	}
	sort.Slice(snapshotUsers, func(i, j int) bool {
		if snapshotUsers[i].Name != snapshotUsers[j].Name {
			return snapshotUsers[i].Name < snapshotUsers[j].Name
		}
		return snapshotUsers[i].Username < snapshotUsers[j].Username
	})

	// 排除名单在采集时固化进快照，使「这个数字是按什么口径算出来的」可追溯。
	excluded := make([]string, 0, len(c.cfg.ExcludeAuthors))
	for _, a := range c.cfg.ExcludeAuthors {
		if n := normalizeString(a); n != "" {
			excluded = append(excluded, n)
		}
	}

	// 未参与豁免名单同样固化：它只改变「未参与成员」名单的构成，
	// 不改变任何统计数据，故不影响统计口径的可比性。
	exempt := make([]string, 0, len(c.cfg.ExemptFromInactive))
	for _, a := range c.cfg.ExemptFromInactive {
		if n := normalizeString(a); n != "" {
			exempt = append(exempt, n)
		}
	}

	fresh := &Snapshot{
		SchemaVersion:      snapshotSchemaVersion,
		GeneratedAt:        started,
		DurationMS:         time.Since(started).Milliseconds(),
		WindowDays:         WindowDays,
		Scope:              "instance",
		CoveredFrom:        coveredFromOf(ranges),
		ScanAllBranches:    c.cfg.ScanAllBranchesOrDefault(),
		ExcludedAuthors:    excluded,
		ExemptFromInactive: exempt,
		Daily:              acc.flatten(),
		Totals: SnapshotMeta{
			ProjectsScanned: len(projects),
			UsersScanned:    len(users),
			ProjectErrors:   failedProjects,
			Users:           snapshotUsers,
		},
	}

	if len(fresh.Daily) == 0 {
		return fresh, ErrSnapshotEmpty
	}

	// 增量合并：本次只覆盖了部分日期区间，更早的历史必须从上一份快照延续下来。
	// 传给合并逻辑的是完整的区间集合而非单个最早起点——区间之间隔着上次采过、
	// 本次跳过的那段历史，只用一个起点概括会把中间那段也当成「已重采」丢掉。
	covered := make([]DateRange, 0, len(ranges))
	for _, r := range ranges {
		covered = append(covered, DateRange{Since: dateKey(r.Since), Until: dateKey(r.Until)})
	}

	// 丢弃落在采集区间之外的记录。
	//
	// GitLab 按绝对时刻过滤，而区间边界以自然日表达：边界附近少数记录可能因
	// 作者所在时区不同而归到相邻日期（实测美西时区的提交被归到区间起点前一天）。
	// 这些日期不在本次替换范围内，其完整数据由上一份快照延续；若让它们进入本次
	// 结果，合并后会与旧记录并存，同一天同一人出现两条，统计随之翻倍。
	kept := fresh.Daily[:0]
	for i := range fresh.Daily {
		if CoversDate(fresh.Daily[i].Date, covered) {
			kept = append(kept, fresh.Daily[i])
		}
	}
	fresh.Daily = kept

	if len(fresh.Daily) == 0 {
		return fresh, ErrSnapshotEmpty
	}

	windowStart := dateKey(startOfDay(time.Now().AddDate(0, 0, -WindowDays)))
	return MergeIncremental(prev, fresh, covered, windowStart), nil
}

// planRanges 计算本次需要采集的日期区间集合。
//
// 两种情形：
//   - 首次采集（prev 不可用）：只取最近一块，更早的历史留给后续运行逐步回填。
//     实测单个超大项目一次拉取一年全分支提交需 10 分钟，不能一次性拉全。
//   - 增量采集：刷新段 +（历史未补满时的）回填段。
//
// 返回的区间两两不重叠：累加器按 (日期, 身份) 归并，
// 区间一旦重叠，同一批提交就会被重复计数。
//
// 所有边界都对齐到自然日的整点（statsLocation 视角）。这一点是必须的：
// 合并以「日期」为替换单位，若采集区间从某天的半途开始，那一天的采集结果
// 只是半天，却会在 covered 里被当成已完整覆盖而丢弃上一份快照中的整天数据，
// 结果日期被截断。对齐后任何一天要么整天采集、要么完全不采。
func (c *Collector) planRanges(prev *Snapshot, now time.Time) []CollectRange {
	windowStartTime := startOfDay(now.AddDate(0, 0, -WindowDays))
	chunkDays := c.cfg.BackfillChunkDaysOr(DefaultBackfillChunkDays)
	overlapDays := c.cfg.RefreshOverlapDaysOr(DefaultRefreshOverlapDays)

	reusable := prev != nil && !prev.SchemaMismatch() && !prev.Empty()
	if !reusable {
		since := startOfDay(now.AddDate(0, 0, -chunkDays))
		if since.Before(windowStartTime) {
			since = windowStartTime
		}
		return []CollectRange{{Since: since, Until: endOfDay(now)}}
	}

	// 刷新段：整体重采最近若干天，吸收追溯推送的提交与近期才合并的 MR。
	refreshSince := startOfDay(prev.GeneratedAt.AddDate(0, 0, -overlapDays))
	if refreshSince.Before(windowStartTime) {
		refreshSince = windowStartTime
	}
	ranges := []CollectRange{{Since: refreshSince, Until: endOfDay(now)}}

	// 回填段：历史若未补满窗口，再向前推进一块。
	// 右端夹到 refreshSince，确保与刷新段不重叠。
	if !prev.CoverageComplete(dateKey(windowStartTime)) {
		backfillUntil := refreshSince
		if prev.CoveredFrom != "" {
			if coveredFrom, err := time.ParseInLocation(dateLayout, prev.CoveredFrom, statsLocation); err == nil {
				backfillUntil = endOfDay(coveredFrom)
				if backfillUntil.After(refreshSince) {
					backfillUntil = refreshSince
				}
			}
		}
		backfillSince := startOfDay(backfillUntil.AddDate(0, 0, -chunkDays))
		if backfillSince.Before(windowStartTime) {
			backfillSince = windowStartTime
		}
		if backfillSince.Before(backfillUntil) {
			ranges = append(ranges, CollectRange{Since: backfillSince, Until: backfillUntil})
		}
	}

	return ranges
}

// coveredFromOf 返回区间集合覆盖到的最早日期，供下次运行判断历史是否补满。
func coveredFromOf(ranges []CollectRange) string {
	earliest := ""
	for _, r := range ranges {
		d := dateKey(r.Since)
		if earliest == "" || d < earliest {
			earliest = d
		}
	}
	return earliest
}

// collectProject 按给定的区间集合采集单个项目。
//
// 区间之间互不重叠，因此同一个项目的多段数据可以安全地累加进同一个累加器。
// 提交采集失败视为整项目失败（提交是主指标）；MR 失败只降级为「无 MR 数据」。
func (c *Collector) collectProject(
	ctx context.Context,
	p Project,
	ranges []CollectRange,
	byEmail, byUsername map[string]GitLabUser,
	acc *accumulator,
) error {
	allBranches := c.cfg.ScanAllBranchesOrDefault()
	var firstErr error

	for _, r := range ranges {
		if err := ctx.Err(); err != nil {
			return err
		}

		commits, err := c.gl.GetCommits(p.ID, r.Since, r.Until, allBranches)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("获取提交失败: %w", err)
			}
			continue
		}

		mrs, err := c.gl.GetMergeRequests(p.ID, r.Since, r.Until)
		if err != nil {
			// MR 维度失败不影响提交统计，降级为「无 MR 数据」。
			log.Printf("[WARN] 项目 [%s] (ID: %d) MR 采集失败，仅统计提交: %v", p.Name, p.ID, err)
			mrs = nil
		}

		c.accumulateProject(commits, mrs, byEmail, byUsername, acc)
	}

	return firstErr
}

// accumulateProject 把单个项目一个区间的原始数据写入累加器。
func (c *Collector) accumulateProject(
	commits []Commit,
	mrs []MergeRequest,
	byEmail, byUsername map[string]GitLabUser,
	acc *accumulator,
) {
	for _, commit := range commits {
		user, _ := ResolveGitLabUser(commit.AuthorEmail, "", byEmail, byUsername)
		commitDate := dateKey(commit.CreatedAt)
		additions, deletions, hasStats := commit.LineStats()

		acc.merge(commitDate, identityKey(user, commit.AuthorEmail, "", commit.AuthorName), func(d *DailyStat) {
			d.Name = pickName(d.Name, user.Name, commit.AuthorName)
			d.Email = pickName(d.Email, user.Email, commit.AuthorEmail)
			d.applyUser(user, c.cfg.GitLabURL)
			d.Commits++
			d.Additions += additions
			d.Deletions += deletions
			// GitLab 未返回 stats 时标记该记录不可信，前端据此提示统计不完整。
			d.StatsIncomplete = d.StatsIncomplete || !hasStats
		})
	}

	for _, mr := range mrs {
		user := c.resolveMRAuthor(mr, byEmail, byUsername, acc)
		// MR 接口不返回作者邮箱，因此归并键退化为用户名（同样唯一且不可变）。
		key := identityKey(user, "", mr.Author.Username, mr.Author.Name)

		acc.merge(dateKey(mr.CreatedAt), key, func(d *DailyStat) {
			d.Name = pickName(d.Name, user.Name, mr.Author.Name)
			d.applyUser(user, c.cfg.GitLabURL)
			d.CreatedMRs++
			switch mr.State {
			case "merged":
				d.MergedMRs++
			case "opened":
				d.OpenedMRs++
			case "closed":
				d.ClosedMRs++
			}
			if mr.State == "merged" && mr.MergedAt != nil {
				d.MergedAt = mr.MergedAt.Format(time.RFC3339)
			}
		})
	}
}

// resolveMRAuthor 在用户索引中匹配 MR 作者；匹配不到时按用户名回查 GitLab 用户详情。
// GitLab 的 MR 列表接口不返回作者邮箱，因此这里额外查一次以保证与提交统计同一口径。
func (c *Collector) resolveMRAuthor(
	mr MergeRequest,
	byEmail, byUsername map[string]GitLabUser,
	acc *accumulator,
) GitLabUser {
	if u, ok := ResolveGitLabUser("", mr.Author.Username, byEmail, byUsername); ok {
		return u
	}
	if mr.Author.Username == "" {
		return GitLabUser{}
	}

	if cached, ok := acc.lookupUser(mr.Author.Username); ok {
		return cached
	}

	user, err := c.gl.GetUserByUsername(mr.Author.Username)
	if err != nil {
		// 回查失败时退化为 MR 自带的用户名，不影响统计口径。
		acc.storeUser(mr.Author.Username, GitLabUser{})
		return GitLabUser{}
	}
	acc.storeUser(mr.Author.Username, user)
	return user
}

func (d *DailyStat) applyUser(user GitLabUser, gitlabURL string) {
	if user.Username != "" {
		d.Username = user.Username
		d.ProfileURL = buildProfileURL(gitlabURL, user.Username)
	}
}

// ResolveGitLabUser 按邮箱 → 用户名匹配 GitLab 用户，匹配一律大小写不敏感。
//
// 刻意不使用姓名匹配：姓名不具备规律（同一人可能写全名/简称/英文名/中文名），
// 实测本实例存在同名对应多个邮箱的情况，按姓名匹配会把不同机器的 root 账号
// 并成一人、把同一人的多个身份拆开，两个方向的错都会发生。
// 邮箱与用户名都是唯一且稳定的维度，足以覆盖实际需要。
func ResolveGitLabUser(email, username string, byEmail, byUsername map[string]GitLabUser) (GitLabUser, bool) {
	if email != "" {
		if u, ok := byEmail[normalizeString(email)]; ok {
			return u, true
		}
	}
	if username != "" {
		if u, ok := byUsername[normalizeString(username)]; ok {
			return u, true
		}
	}
	return GitLabUser{}, false
}

func indexUsers(users []GitLabUser) (byEmail, byUsername map[string]GitLabUser) {
	byEmail = make(map[string]GitLabUser, len(users))
	byUsername = make(map[string]GitLabUser, len(users))
	for _, u := range users {
		if u.Email != "" {
			byEmail[normalizeString(u.Email)] = u
		}
		if u.Username != "" {
			byUsername[normalizeString(u.Username)] = u
		}
	}
	return
}

// identityKey 生成作者聚合键，优先级：GitLab 用户 ID → 邮箱 → 用户名。
//
// 已匹配到 GitLab 用户时用用户 ID，这样同一人用不同邮箱提交也能合并；
// 匹配不到时退化为邮箱（提交场景）或用户名（MR 场景，其接口不返回邮箱）。
// 姓名只作为最后的兜底：实测全库仅 1 条记录三者皆无，
// 用一个统一哨兵值反而会把互不相干的人并到一起，保留姓名更安全。
func identityKey(user GitLabUser, email, username, name string) string {
	if user.ID > 0 {
		return fmt.Sprintf("u:%d", user.ID)
	}
	if email != "" {
		return "e:" + normalizeString(email)
	}
	if username != "" {
		return "un:" + normalizeString(username)
	}
	if name == "" {
		name = "Unknown"
	}
	return "n:" + normalizeString(name)
}

func userIDFromKey(key string) int {
	if !strings.HasPrefix(key, "u:") {
		return 0
	}
	var id int
	if _, err := fmt.Sscanf(key, "u:%d", &id); err != nil {
		return 0
	}
	return id
}

func pickName(existing, primary, fallback string) string {
	if existing != "" {
		return existing
	}
	if primary != "" {
		return primary
	}
	return fallback
}

// accumulator 汇总各并发项目的结果，自身持锁。
type accumulator struct {
	cfg    *Config
	mu     sync.Mutex
	daily  map[string]map[string]*DailyStat
	users  map[string]GitLabUser
	lookup map[string]GitLabUser
}

// merge 对指定日期 + 作者执行增量更新，避免各项目之间互相覆盖。
func (a *accumulator) merge(date, key string, apply func(*DailyStat)) {
	a.mu.Lock()
	defer a.mu.Unlock()

	byDate, ok := a.daily[date]
	if !ok {
		byDate = make(map[string]*DailyStat)
		a.daily[date] = byDate
	}
	entry, ok := byDate[key]
	if !ok {
		entry = &DailyStat{Date: date, AuthorKey: userIDFromKey(key)}
		byDate[key] = entry
	}
	apply(entry)
}

// registerUser 记录全局用户，用于跨项目合并同一作者的展示信息。
func (a *accumulator) registerUser(username string, user GitLabUser) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users[normalizeString(username)] = user
}

func (a *accumulator) lookupUser(username string) (GitLabUser, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.lookup[normalizeString(username)]
	return u, ok
}

func (a *accumulator) storeUser(username string, user GitLabUser) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lookup[normalizeString(username)] = user
}

// flatten 把嵌套 map 摊平成有序切片，保证快照内容可稳定 diff。
func (a *accumulator) flatten() []DailyStat {
	a.mu.Lock()
	defer a.mu.Unlock()

	result := make([]DailyStat, 0, len(a.daily))
	for _, byDate := range a.daily {
		for _, entry := range byDate {
			result = append(result, *entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Date != result[j].Date {
			return result[i].Date < result[j].Date
		}
		if result[i].AuthorKey != result[j].AuthorKey {
			return result[i].AuthorKey < result[j].AuthorKey
		}
		return result[i].Name < result[j].Name
	})
	return result
}
