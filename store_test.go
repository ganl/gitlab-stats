package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSnapshot() *Snapshot {
	return &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		GeneratedAt:   time.Now().Add(-time.Hour),
		WindowDays:    WindowDays,
		Scope:         "instance",
		Totals: SnapshotMeta{
			ProjectsScanned: 2,
			UsersScanned:    4,
			Users: []SnapshotUser{
				{ID: 1, Name: "Alice", Username: "alice", ProfileURL: "https://git.example.com/alice"},
				{ID: 2, Name: "Bob", Username: "bob", ProfileURL: "https://git.example.com/bob"},
				{ID: 3, Name: "Carol", Username: "carol", ProfileURL: "https://git.example.com/carol"},
				{ID: 4, Name: "Dave", Username: "dave", ProfileURL: "https://git.example.com/dave"},
			},
		},
	}
}

// mkDate 生成最近 n 天前的日期键。
func mkDate(daysAgo int) string {
	return dateKey(time.Now().AddDate(0, 0, -daysAgo))
}

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Commits: 3, Additions: 30, Deletions: 5},
	}

	if err := store.Save(snap); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "stats.json")); err != nil {
		t.Fatalf("stats.json not written: %v", err)
	}

	// 新建 Store 模拟进程重启，必须能从磁盘恢复。
	fresh := NewStore(dir)
	loaded, ok, err := fresh.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !ok {
		t.Fatal("expected snapshot to be loaded")
	}
	if len(loaded.Daily) != 1 || loaded.Daily[0].Commits != 3 {
		t.Errorf("unexpected daily data: %+v", loaded.Daily)
	}
	if loaded.Totals.ProjectsScanned != 2 {
		t.Errorf("expected ProjectsScanned=2, got %d", loaded.Totals.ProjectsScanned)
	}
}

func TestStore_LoadMissingFileIsNotError(t *testing.T) {
	store := NewStore(t.TempDir())

	snap, ok, err := store.Load()
	if err != nil {
		t.Fatalf("missing file should not be an error, got: %v", err)
	}
	if ok || snap != nil {
		t.Error("expected no snapshot for empty store")
	}
}

func TestStore_SaveIsAtomicAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	first := sampleSnapshot()
	first.Daily = []DailyStat{{Date: mkDate(2), AuthorKey: 1, Name: "Alice", Commits: 1}}
	if err := store.Save(first); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// 二次覆盖保存必须成功，且目录内不残留临时文件。
	second := sampleSnapshot()
	second.Daily = []DailyStat{{Date: mkDate(1), AuthorKey: 2, Name: "Bob", Commits: 9}}
	if err := store.Save(second); err != nil {
		t.Fatalf("overwrite Save failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	snap, ok := store.Current()
	if !ok || snap.Daily[0].Commits != 9 {
		t.Errorf("expected overwritten snapshot in memory, got %+v", snap)
	}
}

func TestSnapshot_EmptyAndStale(t *testing.T) {
	var nilSnap *Snapshot
	if !nilSnap.Empty() {
		t.Error("nil snapshot should be empty")
	}
	if !nilSnap.Stale() {
		t.Error("nil snapshot should count as stale")
	}

	snap := sampleSnapshot()
	if !snap.Empty() {
		t.Error("snapshot without daily rows should be empty")
	}
	if snap.Stale() {
		t.Error("fresh snapshot should not be stale")
	}

	snap.GeneratedAt = time.Now().Add(-48 * time.Hour)
	if !snap.Stale() {
		t.Error("snapshot older than threshold should be stale")
	}
}

func TestSnapshot_SchemaMismatch(t *testing.T) {
	snap := sampleSnapshot()
	if snap.SchemaMismatch() {
		t.Error("current schema version should not mismatch")
	}

	snap.SchemaVersion = snapshotSchemaVersion - 1
	if !snap.SchemaMismatch() {
		t.Error("older schema version should mismatch")
	}
}

// TestBuildCodeVolume_ExcludesBotsFromInactive 机器人 / 服务账号不得出现在零提交名单中，
// 否则报表会把自动化身份当成未参与开发的成员（真实实例上确实存在这类账号）。
func TestBuildCodeVolume_ExcludesBotsFromInactive(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = append(snap.Totals.Users,
		SnapshotUser{ID: 90, Name: "CodeReview-bot", Username: "group_7_bot_xxx", Bot: true},
		SnapshotUser{ID: 91, Name: "AI-Review", Username: "group_8_bot_yyy", Bot: true},
	)
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 1},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	for _, m := range vol.InactiveMembers {
		if strings.Contains(m.Username, "_bot_") {
			t.Errorf("bot account must not appear as inactive member: %+v", m)
		}
		if m.Username == "CodeReview-bot" || m.Username == "AI-Review" {
			t.Errorf("bot account leaked into inactive list: %+v", m)
		}
	}
	// 该 fixture 中只有 Alice 有提交，Bob / Carol / Dave 均为非 bot 的零提交成员。
	if len(vol.InactiveMembers) != 3 {
		t.Errorf("expected 3 non-bot inactive members, got %d: %+v",
			len(vol.InactiveMembers), vol.InactiveMembers)
	}
}

// TestSnapshotUser_BotFlagSurvivesRoundTrip bot 标记必须能落盘并读回。
func TestSnapshotUser_BotFlagSurvivesRoundTrip(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = []SnapshotUser{
		{ID: 1, Name: "Bot", Username: "some_bot", Bot: true},
		{ID: 2, Name: "Human", Username: "human"},
	}
	snap.Daily = []DailyStat{{Date: mkDate(1), AuthorKey: 2, Name: "Human", Commits: 1}}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !back.Totals.Users[0].Bot {
		t.Error("bot flag lost after round trip")
	}
	if back.Totals.Users[1].Bot {
		t.Error("non-bot user wrongly flagged as bot")
	}
}

// TestDailyStat_CoversAllPanels 验证一份日粒度快照能同时喂给三个统计口径，
// 这是「一次采集服务全部面板」的核心约束。
func TestDailyStat_CoversAllPanels(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{
			Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice",
			ProfileURL: "https://git.example.com/alice",
			Commits:    5, Additions: 100, Deletions: 20,
			CreatedMRs: 2, MergedMRs: 1, OpenedMRs: 1,
			MergedAt: time.Now().AddDate(0, 0, -1).Format(time.RFC3339),
		},
		{
			Date: mkDate(2), AuthorKey: 2, Name: "Bob", Username: "bob",
			ProfileURL: "https://git.example.com/bob",
			Commits:    3, Additions: 50, Deletions: 10,
			CreatedMRs: 1, ClosedMRs: 1,
		},
	}

	freq := BuildCommitFrequency(snap, "day", 90)
	if len(freq) != 2 {
		t.Fatalf("expected 2 commit-frequency buckets, got %d", len(freq))
	}

	mr := BuildMRStatistics(snap, "day", 90, "https://git.example.com")
	if mr.Total != 3 || mr.Merged != 1 || mr.Opened != 1 || mr.Closed != 1 {
		t.Errorf("unexpected MR totals: %+v", mr)
	}
	if len(mr.Authors) != 2 || mr.Authors[0].Name != "Alice" {
		t.Errorf("MR authors should be sorted by count: %+v", mr.Authors)
	}
	// 合并趋势必须按合并日聚合，此处 Alice 与 Bob 各在不同日期。
	if len(mr.MergedByDay) != 1 || mr.MergedByDay[0].Count != 1 {
		t.Errorf("unexpected merged_by_day: %+v", mr.MergedByDay)
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	if vol.TotalCommits != 8 || vol.TotalAdditions != 150 || vol.TotalDeletions != 30 {
		t.Errorf("unexpected code volume: %+v", vol)
	}
	if len(vol.TopContributors) != 2 || vol.TopContributors[0].Name != "Alice" {
		t.Errorf("contributors should be sorted by commits: %+v", vol.TopContributors)
	}
	// Carol / Dave 无任何提交，必须被识别为零提交成员。
	if len(vol.InactiveMembers) != 2 {
		t.Fatalf("expected 2 inactive members, got %d: %+v", len(vol.InactiveMembers), vol.InactiveMembers)
	}
	if vol.InactiveMembers[0].Name != "Carol" || vol.InactiveMembers[1].Name != "Dave" {
		t.Errorf("inactive members should be name-sorted: %+v", vol.InactiveMembers)
	}
}

// TestBuildCommitFrequency_PeriodGrouping 按周 / 按月聚合必须与按日总量一致。
func TestBuildCommitFrequency_PeriodGrouping(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Commits: 2},
		{Date: mkDate(2), AuthorKey: 1, Name: "Alice", Commits: 3},
	}

	total := func(period string) int {
		sum := 0
		for _, item := range BuildCommitFrequency(snap, period, 90) {
			sum += item.Count
		}
		return sum
	}

	for _, period := range []string{"day", "week", "month"} {
		if got := total(period); got != 5 {
			t.Errorf("period=%s: expected total 5, got %d", period, got)
		}
	}
}

// TestBuildCommitFrequency_WindowBoundary 超出窗口的数据不得出现在结果中。
func TestBuildCommitFrequency_WindowBoundary(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(5), AuthorKey: 1, Name: "Alice", Commits: 1},
		{Date: mkDate(200), AuthorKey: 1, Name: "Alice", Commits: 99},
	}

	freq := BuildCommitFrequency(snap, "day", 30)
	if len(freq) != 1 {
		t.Fatalf("expected only in-window data, got %+v", freq)
	}
	if freq[0].Count != 1 {
		t.Errorf("expected count 1, got %d", freq[0].Count)
	}
}

// TestBuildCodeVolume_ZeroCommitIsNotInactive 有提交但 0 行变更的人仍算活跃，
// 这是旧实现容易漏判的场景（GitLab 对空提交可能不返回 stats）。
func TestBuildCodeVolume_ZeroCommitIsNotInactive(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 1},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	for _, m := range vol.InactiveMembers {
		if m.Name == "Alice" {
			t.Error("Alice has commits and must not be reported as inactive")
		}
	}
	if len(vol.InactiveMembers) != 3 {
		t.Errorf("expected 3 inactive members, got %d", len(vol.InactiveMembers))
	}
}

// TestBuildCodeVolume_FallbackUsernameMatch 未匹配到用户 ID 时，用户名仍能用于活跃判定。
func TestBuildCodeVolume_FallbackUsernameMatch(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 0, Name: "bob", Username: "bob", Commits: 4},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	for _, m := range vol.InactiveMembers {
		if m.Username == "bob" {
			t.Error("bob matched by username must not be reported as inactive")
		}
	}
}

// TestBuildCodeVolume_RenameStaysTogether 同一 GitLab 用户改名后仍须合并为一条贡献记录。
// 归并若按姓名做，改名会把同一个人拆成两个贡献者，排行数据随之失真。
func TestBuildCodeVolume_RenameStaysTogether(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = nil
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 42, Name: "Alice Chen", Username: "alice", Commits: 3, Additions: 10, Deletions: 2},
		{Date: mkDate(2), AuthorKey: 42, Name: "Alice C.", Username: "alice", Commits: 5, Additions: 20, Deletions: 4},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	if len(vol.TopContributors) != 1 {
		t.Fatalf("renamed user must collapse into one contributor, got %d: %+v",
			len(vol.TopContributors), vol.TopContributors)
	}
	c := vol.TopContributors[0]
	if c.Commits != 8 || c.Additions != 30 || c.Deletions != 6 {
		t.Errorf("expected merged totals 8/30/6, got %d/%d/%d", c.Commits, c.Additions, c.Deletions)
	}
}

// TestBuildCodeVolume_SameNameDifferentPeople 同名但属于不同 GitLab 账号时不得被合并。
func TestBuildCodeVolume_SameNameDifferentPeople(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = nil
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 11, Name: "张伟", Username: "zhangwei1", Commits: 4},
		{Date: mkDate(1), AuthorKey: 12, Name: "张伟", Username: "zhangwei2", Commits: 9},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	if len(vol.TopContributors) != 2 {
		t.Fatalf("same-name different accounts must stay separate, got %d: %+v",
			len(vol.TopContributors), vol.TopContributors)
	}
	// 按提交数降序，zhangwei2 (9) 在前。
	if vol.TopContributors[0].Username != "zhangwei2" || vol.TopContributors[0].Commits != 9 {
		t.Errorf("unexpected ordering/first contributor: %+v", vol.TopContributors[0])
	}
	if vol.TopContributors[1].Username != "zhangwei1" || vol.TopContributors[1].Commits != 4 {
		t.Errorf("unexpected second contributor: %+v", vol.TopContributors[1])
	}
}

// TestBuildMRStatistics_RenameStaysTogether MR 作者统计同样必须按稳定身份归并。
func TestBuildMRStatistics_RenameStaysTogether(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = nil
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 42, Name: "Alice Chen", Username: "alice", CreatedMRs: 2},
		{Date: mkDate(2), AuthorKey: 42, Name: "Alice C.", Username: "alice", CreatedMRs: 3},
	}

	stats := BuildMRStatistics(snap, "day", 90, "https://git.example.com")
	if len(stats.Authors) != 1 {
		t.Fatalf("renamed MR author must collapse into one entry, got %d: %+v",
			len(stats.Authors), stats.Authors)
	}
	if stats.Authors[0].Count != 5 {
		t.Errorf("expected 5 created MRs after merge, got %d", stats.Authors[0].Count)
	}
}

// TestDailyStat_AuthorKeyAlwaysSerialized author_key=0 必须显式落盘。
// 该字段表示「未匹配到 GitLab 用户」（镜像仓库的上游开源提交者），
// 若被 omitempty 省略，读取方无法区分「未匹配」与「字段缺失」。
func TestDailyStat_AuthorKeyAlwaysSerialized(t *testing.T) {
	d := DailyStat{Date: mkDate(1), Name: "upstream-dev", Email: "dev@freebsd.org", Commits: 1}

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data), `"author_key":0`) {
		t.Errorf("author_key must be serialized even when zero, got %s", data)
	}

	var back DailyStat
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if back.AuthorKey != 0 {
		t.Errorf("expected author_key 0 to round-trip, got %d", back.AuthorKey)
	}
}

// TestExcludeAuthors_FiltersAllAggregations 被排除的自动化身份不得出现在任何统计口径中。
// 场景来源：真实实例上的打包账号每次提交只改版本字符串，
// additions 与 deletions 恒等（每次提交都是同一对增减），但 GitLab 标记 bot=false。
func TestExcludeAuthors_FiltersAllAggregations(t *testing.T) {
	snap := sampleSnapshot()
	snap.Totals.Users = nil
	snap.ExcludedAuthors = []string{"packaging-bot"}
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 10, Additions: 100, Deletions: 20},
		{Date: mkDate(1), AuthorKey: 166, Name: "packaging-bot", Username: "packaging-bot",
			Email: "packaging-bot@example.com", Commits: 50, Additions: 100, Deletions: 100,
			CreatedMRs: 7, MergedMRs: 3},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")
	if vol.TotalCommits != 10 {
		t.Errorf("excluded author's commits must not count, got %d", vol.TotalCommits)
	}
	if vol.TotalAdditions != 100 || vol.TotalDeletions != 20 {
		t.Errorf("excluded author's lines must not count, got +%d/-%d", vol.TotalAdditions, vol.TotalDeletions)
	}
	for _, c := range vol.TopContributors {
		if c.Username == "packaging-bot" {
			t.Error("excluded author must not appear in top contributors")
		}
	}

	mr := BuildMRStatistics(snap, "day", 90, "https://git.example.com")
	if mr.Total != 0 {
		t.Errorf("excluded author's MRs must not count, got total=%d", mr.Total)
	}
	for _, a := range mr.Authors {
		if a.Username == "packaging-bot" {
			t.Error("excluded author must not appear in MR authors")
		}
	}

	freq := BuildCommitFrequency(snap, "day", 90)
	for _, f := range freq {
		if f.Count != 10 {
			t.Errorf("commit frequency should only count Alice's 10 commits, got %d", f.Count)
		}
	}
}

// TestExcludeAuthors_ExcludedUsersNotInactive 用户表也要应用排除名单。
//
// 回归场景（真实实例）：exclude_authors 配了 packaging-bot 与 release-bot，
// 日报记录被正确跳过，但用户表循环只判了 bot——这两个账号因为
// 「窗口内没有任何活跃标记」反而被列进「未参与成员」，与排除意图相反。
func TestExcludeAuthors_ExcludedUsersNotInactive(t *testing.T) {
	snap := sampleSnapshot()
	snap.ExcludedAuthors = []string{"packaging-bot", "release-bot"}
	snap.Totals.Users = []SnapshotUser{
		{ID: 1, Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: 166, Name: "Packager", Username: "packaging-bot", Email: "pack@example.com"},
		{ID: 167, Name: "Release Bot", Username: "release-bot", Email: "robot@example.com"},
		{ID: 168, Name: "GitLab Bot", Username: "gitlab-bot", Email: "bot@example.com", Bot: true},
		{ID: 169, Name: "Bob", Username: "bob", Email: "bob@example.com"},
	}
	// 只有 Alice 有活动；其余用户表成员应被判为未参与，被排除者除外。
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice",
			Email: "alice@example.com", Commits: 10, Additions: 100, Deletions: 20},
	}

	vol := BuildCodeVolume(snap, 90, "https://git.example.com")

	got := make(map[string]bool, len(vol.InactiveMembers))
	for _, m := range vol.InactiveMembers {
		got[m.Username] = true
	}

	if got["packaging-bot"] {
		t.Error("excluded author packaging-bot must not appear as inactive member")
	}
	if got["release-bot"] {
		t.Error("excluded author release-bot must not appear as inactive member")
	}
	if got["gitlab-bot"] {
		t.Error("bot account must not appear as inactive member")
	}
	if got["alice"] {
		t.Error("active member alice must not appear as inactive member")
	}
	if !got["bob"] {
		t.Error("genuinely inactive member bob must still be reported")
	}
	if len(vol.InactiveMembers) != 1 {
		t.Errorf("expected exactly 1 inactive member, got %d: %v", len(vol.InactiveMembers), got)
	}
}

// TestIsIdentityExcluded_MatchesAnyDimension 排除判定对用户名 / 邮箱 / 姓名任一命中即生效。
func TestIsIdentityExcluded_MatchesAnyDimension(t *testing.T) {
	snap := &Snapshot{ExcludedAuthors: []string{"  Release-Bot  ", "pack@x.com"}}

	cases := []struct {
		name                   string
		username, email, name2 string
		want                   bool
	}{
		{"用户名命中（大小写与空白不敏感）", "release-bot", "", "", true},
		{"邮箱命中", "", "PACK@X.COM", "", true},
		{"连字符与空格不同源，不误命中", "", "", "release bot", false},
		{"全部未命中", "alice", "alice@x.com", "Alice", false},
		{"空身份不误判", "", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := snap.IsIdentityExcluded(tc.username, tc.email, tc.name2); got != tc.want {
				t.Errorf("IsIdentityExcluded(%q,%q,%q) = %v, want %v",
					tc.username, tc.email, tc.name2, got, tc.want)
			}
		})
	}

	// 姓名维度同样参与匹配（规范化后精确比对）。
	byName := &Snapshot{ExcludedAuthors: []string{"Release Bot"}}
	if !byName.IsIdentityExcluded("", "", "release bot") {
		t.Error("excluded name must match after normalization")
	}
}

// TestExcludeAuthors_MatchesByEmailOrName 排除名单按用户名、邮箱、姓名任一匹配均可生效。
func TestExcludeAuthors_MatchesByEmailOrName(t *testing.T) {
	base := DailyStat{Date: mkDate(1), Name: "Release Bot", Username: "release-bot",
		Email: "release-bot@example.com", Commits: 5}

	cases := []struct {
		name     string
		excluded []string
		want     bool
	}{
		{"按用户名（大小写不敏感）", []string{"RELEASE-BOT"}, true},
		{"按邮箱", []string{"release-bot@example.com"}, true},
		{"按姓名", []string{"release bot"}, true},
		{"未列入名单", []string{"someone-else"}, false},
		{"空名单", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{ExcludedAuthors: tc.excluded}
			if got := snap.IsAuthorExcluded(&base); got != tc.want {
				t.Errorf("IsAuthorExcluded = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExcludeAuthorSet_Normalizes 配置侧的排除集合必须规范化，忽略空项。
func TestExcludeAuthorSet_Normalizes(t *testing.T) {
	cfg := &Config{ExcludeAuthors: []string{"  Build-Bot  ", "", "release@x.com"}}
	set := cfg.ExcludeAuthorSet()

	if set == nil {
		t.Fatal("expected non-nil set")
	}
	if !set["build-bot"] {
		t.Error("expected normalized 'build-bot' in set")
	}
	if !set["release@x.com"] {
		t.Error("expected 'release@x.com' in set")
	}
	if len(set) != 2 {
		t.Errorf("empty entries must be skipped, got %d entries: %v", len(set), set)
	}

	empty := &Config{}
	if empty.ExcludeAuthorSet() != nil {
		t.Error("nil config authors should yield nil set")
	}
}

// --- 未参与豁免名单（exempt_from_inactive）---

// TestExemptFromInactive_HidesFromInactiveList 豁免账号不出现在未参与名单。
//
// 回归场景（真实实例）：领导（team-lead）不承担日常编码指标，切到较短展示范围
// （最近 30 天）时窗口内没有任何记录，会被列进「未参与成员」并在汇报材料里点名。
func TestExemptFromInactive_HidesFromInactiveList(t *testing.T) {
	snap := sampleSnapshot()
	snap.ExemptFromInactive = []string{"team-lead"}
	snap.Totals.Users = []SnapshotUser{
		{ID: 1, Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: 30, Name: "Team Lead", Username: "team-lead", Email: "team-lead@example.com"},
		{ID: 2, Name: "Bob", Username: "bob", Email: "bob@example.com"},
	}
	// 窗口内只有 Alice 有活动；team-lead 与 bob 都没有。
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice",
			Email: "alice@example.com", Commits: 10, Additions: 100, Deletions: 20},
	}

	vol := BuildCodeVolume(snap, 30, "https://git.example.com")

	got := make(map[string]bool, len(vol.InactiveMembers))
	for _, m := range vol.InactiveMembers {
		got[m.Username] = true
	}
	if got["team-lead"] {
		t.Error("豁免账号 team-lead 不得出现在未参与成员名单")
	}
	if !got["bob"] {
		t.Error("未豁免且确实无活动的 bob 必须照常点名")
	}
	if len(vol.InactiveMembers) != 1 {
		t.Errorf("期望仅 1 名未参与成员，实际 %d: %v", len(vol.InactiveMembers), got)
	}
}

// TestExemptFromInactive_KeepsContributionStats 豁免只影响点名，不影响统计。
//
// 这是与 exclude_authors 的关键差异：后者把账号从全部统计中剔除，
// 前者必须保留提交、代码量与贡献排行——被豁免者偶有的提交仍是真实贡献。
func TestExemptFromInactive_KeepsContributionStats(t *testing.T) {
	snap := sampleSnapshot()
	snap.ExemptFromInactive = []string{"team-lead"}
	snap.Totals.Users = []SnapshotUser{
		{ID: 30, Name: "Team Lead", Username: "team-lead", Email: "team-lead@example.com"},
	}
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 30, Name: "Team Lead", Username: "team-lead",
			Email: "team-lead@example.com", Commits: 5, Additions: 50, Deletions: 10},
	}

	vol := BuildCodeVolume(snap, 30, "https://git.example.com")

	if vol.TotalCommits != 5 || vol.TotalAdditions != 50 || vol.TotalDeletions != 10 {
		t.Errorf("豁免不得影响代码量统计，实际 commits=%d +%d -%d",
			vol.TotalCommits, vol.TotalAdditions, vol.TotalDeletions)
	}
	found := false
	for _, c := range vol.TopContributors {
		if c.Username == "team-lead" {
			found = true
			if c.Commits != 5 {
				t.Errorf("豁免账号的提交数应保留，实际 %d", c.Commits)
			}
		}
	}
	if !found {
		t.Error("豁免账号仍应出现在贡献排行中")
	}
	// 有活动的人本就不会进未参与名单，豁免不应改变这一点。
	if len(vol.InactiveMembers) != 0 {
		t.Errorf("期望无未参与成员，实际 %+v", vol.InactiveMembers)
	}
}

// TestExemptFromInactive_IndependentOfExcludeAuthors 两份名单语义独立、互不干扰。
//
// exclude_authors 命中 → 从全部统计中剔除；exempt_from_inactive 命中 →
// 仅从「未参与成员」名单取下。同一个账号若只进入其中一份，行为必须可预测。
func TestExemptFromInactive_IndependentOfExcludeAuthors(t *testing.T) {
	// 仅被排除：不进任何统计，也不进未参与名单。
	excluded := sampleSnapshot()
	excluded.ExcludedAuthors = []string{"pack-bot"}
	excluded.Totals.Users = []SnapshotUser{
		{ID: 9, Name: "Pack Bot", Username: "pack-bot", Email: "pack@example.com"},
	}
	excluded.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 9, Name: "Pack Bot", Username: "pack-bot",
			Email: "pack@example.com", Commits: 7, Additions: 7, Deletions: 7},
	}

	vol := BuildCodeVolume(excluded, 30, "https://git.example.com")
	if vol.TotalCommits != 0 || len(vol.TopContributors) != 0 {
		t.Errorf("被排除账号不得进入统计: commits=%d contributors=%+v",
			vol.TotalCommits, vol.TopContributors)
	}
	if len(vol.InactiveMembers) != 0 {
		t.Errorf("被排除账号不得出现在未参与名单: %+v", vol.InactiveMembers)
	}

	// 仅被豁免：不点名，统计口径不变。
	exempt := sampleSnapshot()
	exempt.ExemptFromInactive = []string{"team-lead"}
	exempt.Totals.Users = []SnapshotUser{
		{ID: 30, Name: "Team Lead", Username: "team-lead", Email: "team-lead@example.com"},
	}
	vol = BuildCodeVolume(exempt, 30, "https://git.example.com")
	if vol.TotalCommits != 0 || len(vol.InactiveMembers) != 0 {
		t.Errorf("无活动的豁免账号既不产生统计也不被点名: commits=%d inactive=%+v",
			vol.TotalCommits, vol.InactiveMembers)
	}

	// 两份名单都没命中：无活动者照常点名。
	plain := sampleSnapshot()
	plain.Totals.Users = []SnapshotUser{
		{ID: 2, Name: "Bob", Username: "bob", Email: "bob@example.com"},
	}
	plain.Daily = nil
	vol = BuildCodeVolume(plain, 30, "https://git.example.com")
	if len(vol.InactiveMembers) != 1 || vol.InactiveMembers[0].Username != "bob" {
		t.Errorf("未列入任何名单的无活动成员必须点名: %+v", vol.InactiveMembers)
	}
}

// TestIsIdentityExemptFromInactive_MatchesAnyDimension 豁免判定对用户名 / 邮箱 / 姓名任一命中即生效。
func TestIsIdentityExemptFromInactive_MatchesAnyDimension(t *testing.T) {
	snap := &Snapshot{ExemptFromInactive: []string{"  Team-Lead  ", "boss@x.com"}}

	cases := []struct {
		name                   string
		username, email, name2 string
		want                   bool
	}{
		{"用户名命中（大小写与空白不敏感）", "team-lead", "", "", true},
		{"邮箱命中", "", "BOSS@X.COM", "", true},
		{"全部未命中", "alice", "alice@x.com", "Alice", false},
		{"空身份不误判", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := snap.IsIdentityExemptFromInactive(tc.username, tc.email, tc.name2); got != tc.want {
				t.Errorf("IsIdentityExemptFromInactive(%q,%q,%q) = %v, want %v",
					tc.username, tc.email, tc.name2, got, tc.want)
			}
		})
	}

	// 姓名维度同样参与匹配。
	byName := &Snapshot{ExemptFromInactive: []string{"Team Lead"}}
	if !byName.IsIdentityExemptFromInactive("", "", "Team Lead") {
		t.Error("豁免名单的姓名维度必须生效")
	}

	// 空名单等价于不豁免。
	if (&Snapshot{}).IsIdentityExemptFromInactive("team-lead", "team-lead@x.com", "Team Lead") {
		t.Error("空豁免名单不应命中任何身份")
	}
	// nil 快照不应 panic。
	if (*Snapshot)(nil).IsIdentityExemptFromInactive("team-lead", "", "") {
		t.Error("nil 快照必须返回 false")
	}
}

// TestExemptFromInactive_PersistsInSnapshot JSON 往返必须保留豁免名单，
// 否则口径随进程重启丢失，未参与名单会突然多出被豁免的人。
func TestExemptFromInactive_PersistsInSnapshot(t *testing.T) {
	snap := sampleSnapshot()
	snap.ExemptFromInactive = []string{"team-lead", "boss@example.com"}
	snap.Daily = []DailyStat{{Date: mkDate(1), Name: "Alice", Commits: 1}}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data), "exempt_from_inactive") {
		t.Errorf("快照 JSON 必须输出 exempt_from_inactive: %s", string(data))
	}

	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(back.ExemptFromInactive) != 2 || back.ExemptFromInactive[0] != "team-lead" {
		t.Errorf("往返后豁免名单丢失: %+v", back.ExemptFromInactive)
	}
	if !back.IsIdentityExemptFromInactive("team-lead", "", "") {
		t.Error("往返后豁免判定必须仍然生效")
	}

	// 未配置豁免时字段应被省略（omitempty），与旧快照字节兼容。
	plain := sampleSnapshot()
	plain.Daily = []DailyStat{{Date: mkDate(1), Name: "Alice", Commits: 1}}
	data, _ = json.Marshal(plain)
	if strings.Contains(string(data), "exempt_from_inactive") {
		t.Error("空豁免名单应省略字段，避免旧快照 diff 噪声")
	}
}

func TestResolveGitLabUser_Priority(t *testing.T) {
	aliceEmail := GitLabUser{ID: 1, Name: "Alice", Username: "alice", Email: "alice@example.com"}
	byEmail := map[string]GitLabUser{"alice@example.com": aliceEmail}
	byUsername := map[string]GitLabUser{"alice": aliceEmail}

	// 邮箱命中优先。
	u, ok := ResolveGitLabUser("ALICE@example.com", "wrong-user", byEmail, byUsername)
	if !ok || u.ID != 1 {
		t.Errorf("email match should win, got %+v (ok=%v)", u, ok)
	}

	// 邮箱缺失时退到用户名。
	u, ok = ResolveGitLabUser("", "ALICE", byEmail, byUsername)
	if !ok || u.ID != 1 {
		t.Errorf("username match should be used as fallback, got %+v (ok=%v)", u, ok)
	}

	// 全都不匹配时返回 false。
	if _, ok := ResolveGitLabUser("x@y.com", "nobody", byEmail, byUsername); ok {
		t.Error("expected no match")
	}
}

// TestResolveGitLabUser_IgnoresName 姓名不参与匹配。
// 姓名没有规律，实测按姓名匹配会把不同机器的 root 账号并成一人。
func TestResolveGitLabUser_IgnoresName(t *testing.T) {
	alice := GitLabUser{ID: 1, Name: "Alice", Username: "alice", Email: "alice@example.com"}
	byEmail := map[string]GitLabUser{"alice@example.com": alice}
	byUsername := map[string]GitLabUser{"alice": alice}

	// 只提供姓名时不应匹配到任何用户——即便姓名与用户表完全一致。
	if u, ok := ResolveGitLabUser("", "", byEmail, byUsername); ok {
		t.Errorf("empty identifiers must not match, got %+v", u)
	}

	// 同名不同人：用户表里的姓名与提交者姓名相同，但没有邮箱/用户名关联时不能命中。
	byEmailOther := map[string]GitLabUser{"other@example.com": alice}
	if u, ok := ResolveGitLabUser("newcomer@example.com", "", byEmailOther, byUsername); ok {
		t.Errorf("name-only lookalike must not match, got %+v", u)
	}
}

// TestIdentityKey_PrefersUserID 同一人换邮箱提交也必须聚合到同一个键。
func TestIdentityKey_PrefersUserID(t *testing.T) {
	user := GitLabUser{ID: 7, Username: "alice"}

	first := identityKey(user, "alice@example.com", "", "Alice")
	second := identityKey(user, "alice@corp.example.com", "", "Alice")
	if first != second {
		t.Errorf("same user with different emails must share a key: %s vs %s", first, second)
	}

	// 未匹配到用户时退化到邮箱。
	anon1 := identityKey(GitLabUser{}, "a@b.com", "", "Someone")
	anon2 := identityKey(GitLabUser{}, "a@b.com", "", "Someone Else")
	if anon1 != anon2 {
		t.Errorf("unmatched authors with same email should share a key: %s vs %s", anon1, anon2)
	}

	// 无邮箱时（MR 场景）退化到用户名，此时相同的姓名不应造成合并。
	mr1 := identityKey(GitLabUser{}, "", "alice", "Alice")
	mr2 := identityKey(GitLabUser{}, "", "bob", "Alice")
	if mr1 == mr2 {
		t.Errorf("distinct usernames must not share a key: %s vs %s", mr1, mr2)
	}

	if got := userIDFromKey(first); got != 7 {
		t.Errorf("expected user id 7 from key, got %d", got)
	}
	if got := userIDFromKey("e:a@b.com"); got != 0 {
		t.Errorf("expected 0 for non-user key, got %d", got)
	}
}

// TestCommit_StatsDistinguishesMissingFromZero GitLab 未返回 stats 时必须可识别，
// 否则「无 stats」会被静默当成「0 行变更」，导致代码量统计偏低。
func TestCommit_StatsDistinguishesMissingFromZero(t *testing.T) {
	var withStats Commit
	if err := withStats.UnmarshalJSON([]byte(`{
		"id": "abc",
		"created_at": "2026-01-01T10:00:00Z",
		"author_name": "Alice",
		"stats": {"additions": 0, "deletions": 0}
	}`)); err != nil {
		t.Fatalf("unmarshal with stats failed: %v", err)
	}
	// stats 存在但为 0，属于「确实 0 行变更」，必须算作可信数据。
	if !withStats.HasStats() {
		t.Error("stats present but zero must still count as available")
	}
	add, del, ok := withStats.LineStats()
	if !ok || add != 0 || del != 0 {
		t.Errorf("expected (0,0,true), got (%d,%d,%v)", add, del, ok)
	}

	var withoutStats Commit
	if err := withoutStats.UnmarshalJSON([]byte(`{
		"id": "def",
		"created_at": "2026-01-01T10:00:00Z",
		"author_name": "Bob"
	}`)); err != nil {
		t.Fatalf("unmarshal without stats failed: %v", err)
	}
	if withoutStats.HasStats() {
		t.Error("missing stats must not be treated as available")
	}
	if _, _, ok := withoutStats.LineStats(); ok {
		t.Error("LineStats must report ok=false when stats are missing")
	}

	// 显式 null 与字段缺失等价。
	var nullStats Commit
	if err := nullStats.UnmarshalJSON([]byte(`{
		"id": "ghi",
		"created_at": "2026-01-01T10:00:00Z",
		"author_name": "Carol",
		"stats": null
	}`)); err != nil {
		t.Fatalf("unmarshal with null stats failed: %v", err)
	}
	if nullStats.HasStats() {
		t.Error("null stats must be treated as missing")
	}
}

// TestCommit_MarshalRoundTripPreservesStats 序列化再反序列化必须保留 stats 语义，
// 否则测试替身与缓存会把「有 stats」丢失成「无 stats」。
func TestCommit_MarshalRoundTripPreservesStats(t *testing.T) {
	original := NewCommitWithStats("abc", time.Now(), "Alice", "alice@example.com", 42, 7)

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data), `"stats"`) {
		t.Errorf("marshaled JSON must snake_case the stats key, got %s", string(data))
	}
	if strings.Contains(string(data), `"Stats"`) {
		t.Errorf("marshaled JSON leaked Go field name: %s", string(data))
	}

	var back Commit
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	add, del, ok := back.LineStats()
	if !ok || add != 42 || del != 7 {
		t.Errorf("round trip lost stats: (%d,%d,%v)", add, del, ok)
	}

	// 无 stats 的提交往返后仍应被判定为缺失。
	without := NewCommitWithoutStats("def", time.Now(), "Bob", "bob@example.com")
	data, _ = json.Marshal(without)
	var backWithout Commit
	if err := json.Unmarshal(data, &backWithout); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if backWithout.HasStats() {
		t.Error("commit without stats must stay without stats after round trip")
	}
}

// --- 增量合并 ---

// TestMergeIncremental_ReplacesOverlappingDates 增量合并必须以整日为替换单位。
//
// 关键风险是重复计数：同一天的记录若既保留旧值又追加新值，
// 该日期的提交与行数就会被算两遍。因此重叠日期必须由新数据整体取代。
func TestMergeIncremental_ReplacesOverlappingDates(t *testing.T) {
	old := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-01-01",
		Daily: []DailyStat{
			{Date: "2025-12-01", Name: "expired", Commits: 9}, // 早于窗口起点 → 丢弃
			{Date: "2026-05-01", Name: "kept", Commits: 5},    // 不在本次区间内 → 保留
			{Date: "2026-06-10", Name: "stale", Commits: 7},   // 落在本次区间内 → 被取代
		},
	}
	fresh := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-06-05",
		Daily: []DailyStat{
			{Date: "2026-06-10", Name: "fresh", Commits: 3},
			{Date: "2026-06-12", Name: "new", Commits: 1},
		},
	}

	// 窗口起点设为 2026-01-01：2025-12-01 落在窗口外应被裁掉，
	// 2026-05-01 在窗口内但不在本次区间内，必须保留。
	got := MergeIncremental(old, fresh,
		[]DateRange{{Since: "2026-06-05", Until: "2026-06-30"}}, "2026-01-01")

	byDate := map[string]DailyStat{}
	for _, d := range got.Daily {
		byDate[d.Date] = d
	}

	if _, ok := byDate["2025-12-01"]; ok {
		t.Error("早于窗口起点的旧记录必须被丢弃")
	}
	if d, ok := byDate["2026-05-01"]; !ok || d.Name != "kept" || d.Commits != 5 {
		t.Errorf("不在本次区间内的记录必须原样保留: %+v", byDate)
	}
	if d, ok := byDate["2026-06-10"]; !ok || d.Name != "fresh" || d.Commits != 3 {
		t.Errorf("重叠日期必须由新数据取代，不得累加: %+v", d)
	}
	if d, ok := byDate["2026-06-12"]; !ok || d.Name != "new" {
		t.Errorf("新日期必须写入: %+v", d)
	}
	if len(got.Daily) != 3 {
		t.Errorf("期望 3 条记录（1 保留 + 2 新增），实际 %d: %+v", len(got.Daily), got.Daily)
	}

	// 合并后按日期升序，便于快照比对。
	for i := 1; i < len(got.Daily); i++ {
		if got.Daily[i].Date < got.Daily[i-1].Date {
			t.Errorf("合并结果必须按日期升序: %+v", got.Daily)
			break
		}
	}
}

// TestMergeIncremental_CoveredFromClampedToWindow 覆盖起点不得早于窗口起点。
//
// 滑出窗口的旧记录已被裁掉，若仍宣称覆盖它们，下次运行会误判历史已补满而停止回填。
func TestMergeIncremental_CoveredFromClampedToWindow(t *testing.T) {
	old := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2025-01-01", // 远早于窗口起点
		Daily:         []DailyStat{{Date: "2026-06-09", Commits: 1}},
	}
	fresh := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-06-05",
		Daily:         []DailyStat{{Date: "2026-06-10", Commits: 1}},
	}

	got := MergeIncremental(old, fresh,
		[]DateRange{{Since: "2026-06-05", Until: "2026-06-30"}}, "2026-06-01")

	if got.CoveredFrom != "2026-06-01" {
		t.Errorf("覆盖起点应夹到窗口起点 2026-06-01，实际 %q", got.CoveredFrom)
	}
	if !got.CoverageComplete("2026-06-01") {
		t.Error("夹紧后应判定为覆盖完整")
	}
	if got.CoverageComplete("2026-05-01") {
		t.Error("窗口更早时不应判定为覆盖完整")
	}
}

// TestMergeIncremental_KeepsEarlierCoverage 回填推进时应保留更早的覆盖起点。
func TestMergeIncremental_KeepsEarlierCoverage(t *testing.T) {
	old := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-05-01",
		Daily:         []DailyStat{{Date: "2026-05-02", Commits: 1}},
	}
	fresh := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-06-05", // 本次只采了最近区间
		Daily:         []DailyStat{{Date: "2026-06-10", Commits: 1}},
	}

	got := MergeIncremental(old, fresh,
		[]DateRange{{Since: "2026-06-05", Until: "2026-06-30"}}, "2026-01-01")
	if got.CoveredFrom != "2026-05-01" {
		t.Errorf("应保留更早的覆盖起点 2026-05-01，实际 %q", got.CoveredFrom)
	}
}

// TestMergeIncremental_FallsBackToFresh 首采 / 口径不匹配时直接采用新快照。
func TestMergeIncremental_FallsBackToFresh(t *testing.T) {
	fresh := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		Daily:         []DailyStat{{Date: "2026-06-10", Commits: 1}},
	}

	for _, old := range []*Snapshot{
		nil,
		{SchemaVersion: snapshotSchemaVersion}, // 空快照
		{SchemaVersion: snapshotSchemaVersion - 1, Daily: []DailyStat{{Date: "2026-01-01"}}}, // 口径不匹配
	} {
		got := MergeIncremental(old, fresh,
			[]DateRange{{Since: "2026-06-05", Until: "2026-06-30"}}, "2026-01-01")
		if len(got.Daily) != 1 || got.Daily[0].Date != "2026-06-10" {
			t.Errorf("应直接采用新快照，实际 %+v", got.Daily)
		}
	}
}

// TestMergeIncremental_PreservesGapBetweenRanges 区间之间的历史必须保留。
//
// 回归场景（真实实例）：增量采集一次会产出两段互不相邻的区间——
// 最近的刷新段，和更早的一块回填段。两段中间的历史本次不重采，但它
// 既不属于任一区间，就必须原样保留下来。
//
// 旧实现只取「本次区间的最早起点」当作单一替换边界，把中间那段也一并丢弃，
// 而新采集里并没有它的数据 —— 结果是快照中间空出整整一块，
// 但 covered_from 仍然宣称已覆盖。实测在真实实例上曾整段丢掉 30 天数据。
func TestMergeIncremental_PreservesGapBetweenRanges(t *testing.T) {
	old := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-06-16",
		Daily: []DailyStat{
			{Date: "2026-06-20", Name: "backfill-old", Commits: 5}, // 回填段内 → 被取代
			{Date: "2026-07-20", Name: "gap", Commits: 11},         // 两段之间 → 必须保留
			{Date: "2026-08-10", Name: "gap", Commits: 13},         // 两段之间 → 必须保留
			{Date: "2026-08-20", Name: "refresh-old", Commits: 7},  // 刷新段内 → 被取代
		},
	}
	fresh := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		CoveredFrom:   "2026-06-16",
		Daily: []DailyStat{
			{Date: "2026-06-20", Name: "backfill-new", Commits: 4},
			{Date: "2026-08-20", Name: "refresh-new", Commits: 6},
		},
	}

	// 本次实际采集的两段：回填段 [06-16, 07-15]、刷新段 [08-15, 09-14]。
	// 二者之间 [07-16, 08-14] 是空洞，本次没有重采。
	covered := []DateRange{
		{Since: "2026-06-16", Until: "2026-07-15"},
		{Since: "2026-08-15", Until: "2026-09-14"},
	}

	got := MergeIncremental(old, fresh, covered, "2025-09-14")

	byDate := map[string]DailyStat{}
	for _, d := range got.Daily {
		byDate[d.Date] = d
	}

	if d, ok := byDate["2026-07-20"]; !ok || d.Commits != 11 {
		t.Errorf("区间之间的历史必须保留，2026-07-20 丢失或损坏: %+v", d)
	}
	if d, ok := byDate["2026-08-10"]; !ok || d.Commits != 13 {
		t.Errorf("区间之间的历史必须保留，2026-08-10 丢失或损坏: %+v", d)
	}
	if d, ok := byDate["2026-06-20"]; !ok || d.Name != "backfill-new" || d.Commits != 4 {
		t.Errorf("回填段内的日期必须由新数据取代: %+v", d)
	}
	if d, ok := byDate["2026-08-20"]; !ok || d.Name != "refresh-new" || d.Commits != 6 {
		t.Errorf("刷新段内的日期必须由新数据取代: %+v", d)
	}
	if len(got.Daily) != 4 {
		t.Errorf("期望 4 条记录（2 保留 + 2 取代），实际 %d: %+v", len(got.Daily), got.Daily)
	}
	// 提交总数：11 + 13（保留）+ 4 + 6（新）= 34。若中间段被丢弃会低于此值。
	total := 0
	for _, d := range got.Daily {
		total += d.Commits
	}
	if total != 34 {
		t.Errorf("区间之间的提交不得丢失，期望合计 34，实际 %d", total)
	}
}

// TestCoversDate_Boundaries 区间判定为闭区间，且空集合不匹配任何日期。
func TestCoversDate_Boundaries(t *testing.T) {
	ranges := []DateRange{{Since: "2026-06-16", Until: "2026-07-15"}}

	cases := []struct {
		date string
		want bool
	}{
		{"2026-06-15", false},
		{"2026-06-16", true}, // 起点含
		{"2026-07-01", true},
		{"2026-07-15", true}, // 终点含
		{"2026-07-16", false},
	}
	for _, tc := range cases {
		if got := CoversDate(tc.date, ranges); got != tc.want {
			t.Errorf("CoversDate(%s) = %v, want %v", tc.date, got, tc.want)
		}
	}

	if CoversDate("2026-07-01", nil) {
		t.Error("空区间集合不应匹配任何日期")
	}
}

// --- 展示窗口口径 ---

// TestCutoffDate_CoversExactlyDays 窗口应为「含今天在内的 N 个自然日」。
//
// 回归场景（真实实例）：旧实现 cutoffDate(days) = now - days，使「最近 7 天」
// 实际覆盖 8 个自然日。用独立脚本按自然日复算时发现对不上——7 天窗口
// 脚本 133 提交 vs 接口 213，差额 80 正是第 8 天（2026-09-07）的量。
func TestCutoffDate_CoversExactlyDays(t *testing.T) {
	cases := []struct {
		days    int
		wantAgo int
	}{
		{1, 0}, // 只有今天
		{7, 6}, // 含今天在内 7 天
		{30, 29},
		{90, 89},
		{365, 364},
	}
	for _, tc := range cases {
		want := dateKey(time.Now().AddDate(0, 0, -tc.wantAgo))
		if got := cutoffDate(tc.days); got != want {
			t.Errorf("cutoffDate(%d) = %s, 期望 %s（%d 天前）",
				tc.days, got, want, tc.wantAgo)
		}
	}

	// 下界兜底：days < 1 不得产生未来日期，否则窗口会静默返回空结果。
	for _, d := range []int{0, -1, -100} {
		if got := cutoffDate(d); got > dateKey(time.Now()) {
			t.Errorf("cutoffDate(%d) = %s 落在未来", d, got)
		}
	}
}

// TestWithinWindow_Boundaries 窗口为闭区间，且「第 N 天前」必须落在窗口外。
func TestWithinWindow_Boundaries(t *testing.T) {
	today := dateKey(time.Now())

	if !withinWindow(today, 7) {
		t.Error("今天必须落在窗口内")
	}
	if !withinWindow(dateKey(time.Now().AddDate(0, 0, -6)), 7) {
		t.Error("6 天前应落在最近 7 天窗口内")
	}
	if withinWindow(dateKey(time.Now().AddDate(0, 0, -7)), 7) {
		t.Error("7 天前应落在最近 7 天窗口外（旧实现的 off-by-one）")
	}

	// days=1 只含今天。
	if withinWindow(dateKey(time.Now().AddDate(0, 0, -1)), 1) {
		t.Error("最近 1 天不应包含昨天")
	}
	if !withinWindow(today, 1) {
		t.Error("最近 1 天必须包含今天")
	}
}
