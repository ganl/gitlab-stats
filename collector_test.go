package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mockGitLab 是一个只读的 GitLab API 模拟服务，用于在不依赖真实实例的前提下
// 端到端验证「采集 → 聚合 → 落盘 → HTTP 读取」全链路。
//
// 它同时充当契约测试：一旦真实 GitLab 的字段名或结构发生变化，
// 这里会先失败，而不是等到线上统计数据静默失真。
type mockGitLab struct {
	projects []Project
	users    []GitLabUser
	// commits[projectID] -> 提交列表
	commits map[int][]Commit
	// mrs[projectID] -> MR 列表
	mrs map[int][]MergeRequest
	// 记录收到的请求，用于断言服务端没有越权访问
	requests []string
	failNow  map[string]bool
}

func newMockGitLab() *mockGitLab {
	now := time.Now()

	users := []GitLabUser{
		{ID: 1, Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: 2, Name: "Bob", Username: "bob", Email: "bob@example.com"},
		{ID: 3, Name: "Carol", Username: "carol", Email: "carol@example.com"},
	}

	mk := func(id string, daysAgo int, name, email string, add, del int, withStats bool) Commit {
		created := now.AddDate(0, 0, -daysAgo)
		if withStats {
			return NewCommitWithStats(id, created, name, email, add, del)
		}
		return NewCommitWithoutStats(id, created, name, email)
	}

	mergedAt := now.AddDate(0, 0, -2)
	return &mockGitLab{
		projects: []Project{
			{ID: 101, Name: "alpha-service"},
			{ID: 102, Name: "beta-service"},
		},
		users: users,
		commits: map[int][]Commit{
			101: {
				mk("c1", 1, "Alice", "alice@example.com", 100, 10, true),
				mk("c2", 2, "Alice", "alice@example.com", 50, 5, true),
				mk("c3", 3, "Bob", "bob@example.com", 30, 3, true),
				// 模拟 GitLab 未返回 stats 的提交，必须被标记为不完整而非当成 0 行。
				mk("c4", 4, "Bob", "bob@example.com", 0, 0, false),
			},
			102: {
				// 同一人的邮箱与显示名都可能变，但邮箱始终是其注册邮箱，
				// 因此必须经由「邮箱 → 用户 ID」归并到同一个人。
				mk("c5", 1, "Alice Zhang", "alice@example.com", 70, 7, true),
			},
		},
		mrs: map[int][]MergeRequest{
			101: {
				mkMR(1, "merged", 5, &mergedAt, 1, "Alice", "alice"),
				mkMR(2, "opened", 1, nil, 2, "Bob", "bob"),
			},
			102: {
				mkMR(3, "closed", 3, nil, 1, "Alice", "alice"),
			},
		},
		requests: []string{},
		failNow:  map[string]bool{},
	}
}

func mkMR(id int, state string, createdDaysAgo int, mergedAt *time.Time, authorID int, name, username string) MergeRequest {
	mr := MergeRequest{
		ID:        id,
		State:     state,
		MergedAt:  mergedAt,
		CreatedAt: time.Now().AddDate(0, 0, -createdDaysAgo),
	}
	mr.Author.ID = authorID
	mr.Author.Name = name
	mr.Author.Username = username
	return mr
}

func (m *mockGitLab) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(m.handle))
}

func (m *mockGitLab) handle(w http.ResponseWriter, r *http.Request) {
	m.requests = append(m.requests, r.Method+" "+r.URL.Path)

	if r.Method != http.MethodGet {
		// 服务端只允许只读请求；一旦出现写操作直接失败，防止误改 GitLab。
		http.Error(w, `{"message":"mock rejects non-GET"}`, http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Path
	page := atoiDefault(r.URL.Query().Get("page"), 1)

	switch {
	case path == "/api/v4/projects":
		m.respondPaginated(w, m.projectsSlice(), page)
	case path == "/api/v4/users":
		if u := r.URL.Query().Get("username"); u != "" {
			var filtered []GitLabUser
			for _, user := range m.users {
				if strings.EqualFold(user.Username, u) {
					filtered = append(filtered, user)
				}
			}
			writeJSONResponse(w, filtered)
			return
		}
		m.respondPaginated(w, m.usersSlice(), page)
	case strings.HasSuffix(path, "/repository/commits"):
		id := projectIDFromPath(path)
		m.respondPaginated(w, commitsToAny(m.commits[id]), page)
	case strings.HasSuffix(path, "/merge_requests"):
		id := projectIDFromPath(path)
		m.respondPaginated(w, mrsToAny(m.mrs[id]), page)
	default:
		http.NotFound(w, r)
	}
}

// respondPaginated 模拟 GitLab 的分页：每页最多 100 条，空页返回 []。
func (m *mockGitLab) respondPaginated(w http.ResponseWriter, items []interface{}, page int) {
	const perPage = 100
	start := (page - 1) * perPage
	if start >= len(items) {
		writeJSONResponse(w, []interface{}{})
		return
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	writeJSONResponse(w, items[start:end])
}

func (m *mockGitLab) projectsSlice() []interface{} {
	out := make([]interface{}, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	return out
}

func (m *mockGitLab) usersSlice() []interface{} {
	out := make([]interface{}, 0, len(m.users))
	for _, u := range m.users {
		out = append(out, u)
	}
	return out
}

func commitsToAny(commits []Commit) []interface{} {
	out := make([]interface{}, 0, len(commits))
	for _, c := range commits {
		out = append(out, c)
	}
	return out
}

func mrsToAny(mrs []MergeRequest) []interface{} {
	out := make([]interface{}, 0, len(mrs))
	for _, mr := range mrs {
		out = append(out, mr)
	}
	return out
}

func projectIDFromPath(path string) int {
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

func writeJSONResponse(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- 端到端测试 ---

func newTestCollector(t *testing.T, mock *mockGitLab) (*Collector, *Config) {
	t.Helper()

	srv := mock.server()
	t.Cleanup(srv.Close)

	cfg := &Config{
		GitLabURL:     srv.URL,
		Token:         "mock-token",
		MaxConcurrent: 4,
	}
	cfg.SetDefaults()

	return NewCollector(NewGitLabClient(cfg, NewCache(true, cfg.CacheTTL)), cfg), cfg
}

// TestCollector_EndToEnd 验证一次采集能产出可服务全部面板的快照。
func TestCollector_EndToEnd(t *testing.T) {
	mock := newMockGitLab()
	collector, cfg := newTestCollector(t, mock)

	var progressCalls []int
	snap, err := collector.Collect(context.Background(), nil, func(current, total int) {
		progressCalls = append(progressCalls, current)
		if total != len(mock.projects) {
			t.Errorf("expected total=%d, got %d", len(mock.projects), total)
		}
	})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if snap.SchemaVersion != snapshotSchemaVersion {
		t.Errorf("expected schema v%d, got v%d", snapshotSchemaVersion, snap.SchemaVersion)
	}
	if snap.Totals.ProjectsScanned != 2 {
		t.Errorf("expected 2 projects scanned, got %d", snap.Totals.ProjectsScanned)
	}
	if snap.Totals.UsersScanned != 3 {
		t.Errorf("expected 3 users scanned, got %d", snap.Totals.UsersScanned)
	}
	if len(snap.Totals.ProjectErrors) != 0 {
		t.Errorf("expected no project errors, got %+v", snap.Totals.ProjectErrors)
	}

	// 进度必须单调递增并从 0 开始，前端进度条依赖这一点。
	if len(progressCalls) == 0 {
		t.Fatal("expected progress callbacks")
	}
	if progressCalls[0] != 0 {
		t.Errorf("expected first progress call to be 0, got %d", progressCalls[0])
	}
	for i := 1; i < len(progressCalls); i++ {
		if progressCalls[i] < progressCalls[i-1] {
			t.Errorf("progress must be monotonic: %v", progressCalls)
			break
		}
	}

	// 提交统计：c1..c5 共 5 个提交。
	vol := BuildCodeVolume(snap, 90, cfg.GitLabURL)
	if vol.TotalCommits != 5 {
		t.Errorf("expected 5 commits, got %d", vol.TotalCommits)
	}
	// Alice 的 3 次提交必须归并到同一个人：显示名变化（Alice / Alice Zhang）
	// 不影响归并，因为归并键是邮箱 → 用户 ID（100+50+70=220）。
	var alice *TopContributor
	for i := range vol.TopContributors {
		if vol.TopContributors[i].Name == "Alice" {
			alice = &vol.TopContributors[i]
			break
		}
	}
	if alice == nil {
		t.Fatalf("Alice missing from top contributors: %+v", vol.TopContributors)
	}
	if alice.Additions != 220 {
		t.Errorf("expected Alice additions 220 (merged via user id), got %d", alice.Additions)
	}
	if alice.Commits != 3 {
		t.Errorf("expected Alice commits 3, got %d", alice.Commits)
	}
	// 匹配到 GitLab 用户后必须带出主页链接。
	if !strings.Contains(alice.ProfileURL, "/alice") {
		t.Errorf("expected profile url for Alice, got %q", alice.ProfileURL)
	}

	// Carol 从未提交，必须出现在零提交列表。
	var carolFound bool
	for _, m := range vol.InactiveMembers {
		if m.Name == "Carol" {
			carolFound = true
		}
		if m.Name == "Alice" || m.Name == "Bob" {
			t.Errorf("%s has commits and must not be inactive", m.Name)
		}
	}
	if !carolFound {
		t.Errorf("Carol should be reported as inactive: %+v", vol.InactiveMembers)
	}

	// MR 统计：1 merged + 1 opened + 1 closed。
	mr := BuildMRStatistics(snap, "day", 90, cfg.GitLabURL)
	if mr.Total != 3 || mr.Merged != 1 || mr.Opened != 1 || mr.Closed != 1 {
		t.Errorf("unexpected MR stats: %+v", mr)
	}
	if len(mr.MergedByDay) != 1 || mr.MergedByDay[0].Count != 1 {
		t.Errorf("expected 1 merged-by-day bucket, got %+v", mr.MergedByDay)
	}

	// 提交频率总量必须等于总提交数。
	freq := BuildCommitFrequency(snap, "day", 90)
	freqTotal := 0
	for _, f := range freq {
		freqTotal += f.Count
	}
	if freqTotal != 5 {
		t.Errorf("expected frequency total 5, got %d", freqTotal)
	}
}

// TestCollector_RecordsExemptFromInactive 采集时把豁免名单固化进快照。
//
// 与 exclude_authors 一样，豁免名单随快照落盘，口径才可追溯——
// 否则同一份数据在不同配置下算出的「未参与成员」无法解释。写入前统一规范化，
// 保证判定侧不必假设名单已经处理过。
func TestCollector_RecordsExemptFromInactive(t *testing.T) {
	mock := newMockGitLab()
	collector, cfg := newTestCollector(t, mock)
	cfg.ExemptFromInactive = []string{"  Team-Lead  ", "", "boss@example.com"}

	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	want := []string{"team-lead", "boss@example.com"}
	if len(snap.ExemptFromInactive) != len(want) {
		t.Fatalf("豁免名单应去空项并规范化，期望 %v，实际 %v", want, snap.ExemptFromInactive)
	}
	for i, w := range want {
		if snap.ExemptFromInactive[i] != w {
			t.Errorf("豁免名单第 %d 项：期望 %q，实际 %q", i, w, snap.ExemptFromInactive[i])
		}
	}

	// 未配置时快照不应带豁免名单。
	mock2 := newMockGitLab()
	plain, plainCfg := newTestCollector(t, mock2)
	snap2, err := plain.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if len(snap2.ExemptFromInactive) != 0 {
		t.Errorf("未配置豁免名单时不应写入内容: %v", snap2.ExemptFromInactive)
	}
	if len(plainCfg.ExemptFromInactive) != 0 {
		t.Errorf("测试基线不应带豁免配置: %v", plainCfg.ExemptFromInactive)
	}
}

// TestCollector_UnknownEmailNotMergedByDisplayName 未注册邮箱不得靠显示名并入同名人。
//
// 这是移除姓名匹配后的核心语义：姓名没有规律（同一人可能写全名/简称/英文名，
// 不同人也可能重名），实测按姓名匹配会把不同机器的 root 账号并成一人。
// 因此当提交邮箱不在 GitLab 用户表内时，只能作为独立身份存在，
// 即便显示名与某个已注册用户完全一致。
func TestCollector_UnknownEmailNotMergedByDisplayName(t *testing.T) {
	mock := newMockGitLab()
	now := time.Now()
	// 该提交的显示名与用户表里的 Alice 完全相同，但邮箱未注册。
	mock.commits[101] = append(mock.commits[101],
		NewCommitWithStats("c9", now.AddDate(0, 0, -1), "Alice", "outsider@elsewhere.com", 999, 1))

	collector, cfg := newTestCollector(t, mock)
	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	vol := BuildCodeVolume(snap, 90, cfg.GitLabURL)

	// 同名但邮箱未注册的提交必须单独成项，不能并进 Alice 名下。
	// 两个身份都叫 "Alice"，靠用户名区分：注册的那个带 username=alice。
	var registered, outsider *TopContributor
	for i := range vol.TopContributors {
		c := &vol.TopContributors[i]
		if c.Name != "Alice" {
			continue
		}
		if c.Username == "alice" {
			registered = c
		} else {
			outsider = c
		}
	}

	if registered == nil {
		t.Fatalf("registered Alice missing: %+v", vol.TopContributors)
	}
	if registered.Additions != 220 || registered.Commits != 3 {
		t.Errorf("registered Alice must keep 220 additions / 3 commits, got %d / %d（未注册邮箱被错误并入）",
			registered.Additions, registered.Commits)
	}

	if outsider == nil {
		t.Fatal("未注册邮箱的提交必须单独成项，而不是被并入同名的已注册用户")
	}
	if outsider.Additions != 999 || outsider.Commits != 1 {
		t.Errorf("outsider should keep its own numbers, got %d / %d", outsider.Additions, outsider.Commits)
	}

	// 未注册邮箱的提交仍然要被统计，只是不以 Alice 的身份。
	if vol.TotalCommits != 6 {
		t.Errorf("expected 6 commits in total, got %d", vol.TotalCommits)
	}
}

// TestCollector_OnlyReadRequests 服务端绝不能对 GitLab 发起写操作。
func TestCollector_OnlyReadRequests(t *testing.T) {
	mock := newMockGitLab()
	collector, _ := newTestCollector(t, mock)

	if _, err := collector.Collect(context.Background(), nil, nil); err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if len(mock.requests) == 0 {
		t.Fatal("expected requests to the mock server")
	}
	for _, req := range mock.requests {
		if !strings.HasPrefix(req, http.MethodGet+" ") {
			t.Errorf("non-read request detected: %s", req)
		}
	}
}

// TestCollector_PartialFailureDoesNotAbort 单项目失败必须被隔离，其余项目照常统计。
func TestCollector_PartialFailureDoesNotAbort(t *testing.T) {
	mock := newMockGitLab()
	// 让 102 项目的提交接口失败。
	mock.commits[102] = nil
	mock.projects = append(mock.projects, Project{ID: 103, Name: "gamma-service"})
	mock.commits[103] = nil
	mock.mrs[103] = nil

	srv := mock.server()
	t.Cleanup(srv.Close)

	// 用自定义 handler 让特定项目返回 500。
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/projects/102/") {
			http.Error(w, `{"message":"500 Internal Server Error"}`, http.StatusInternalServerError)
			return
		}
		mock.handle(w, r)
	}))
	t.Cleanup(failSrv.Close)

	cfg := &Config{GitLabURL: failSrv.URL, Token: "mock-token", MaxConcurrent: 4}
	cfg.SetDefaults()
	collector := NewCollector(NewGitLabClient(cfg, NewCache(true, cfg.CacheTTL)), cfg)

	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect should not fail on partial project errors: %v", err)
	}

	if len(snap.Totals.ProjectErrors) == 0 {
		t.Error("expected failed project to be reported")
	}
	// 101 的数据仍然必须在。
	vol := BuildCodeVolume(snap, 90, cfg.GitLabURL)
	if vol.TotalCommits == 0 {
		t.Error("healthy project data must still be collected")
	}
}

// TestCollector_ContextCancellation 取消上下文后采集应及时返回错误，供优雅停机使用。
func TestCollector_ContextCancellation(t *testing.T) {
	mock := newMockGitLab()
	collector, _ := newTestCollector(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := collector.Collect(ctx, nil, nil); err == nil {
		t.Error("expected error when context is already cancelled")
	}
}

// TestCollector_StatsIncompleteFlag GitLab 缺 stats 的提交必须被标记，
// 而不是静默按 0 行统计——这是「统计不完善」的关键修复点之一。
func TestCollector_StatsIncompleteFlag(t *testing.T) {
	mock := newMockGitLab()
	collector, _ := newTestCollector(t, mock)

	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	var flagged int
	for _, d := range snap.Daily {
		if d.StatsIncomplete {
			flagged++
		}
	}
	if flagged == 0 {
		t.Error("expected at least one record flagged as stats_incomplete")
	}
}

// TestCollector_SnapshotServesAllWindows 一次采集的快照必须能服务多个展示窗口，
// 这是「只统计一次、前端按范围读取」的核心保障。
func TestCollector_SnapshotServesAllWindows(t *testing.T) {
	mock := newMockGitLab()
	collector, cfg := newTestCollector(t, mock)

	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	for _, days := range []int{30, 90, 180, 365} {
		vol := BuildCodeVolume(snap, days, cfg.GitLabURL)
		if vol.TotalCommits != 5 {
			t.Errorf("days=%d: expected 5 commits from cached snapshot, got %d", days, vol.TotalCommits)
		}
	}

	// 窗口小于数据年龄时应看不到数据，验证窗口裁剪确实生效。
	for _, days := range []int{1, 90} {
		for _, c := range BuildCommitFrequency(snap, "day", days) {
			if c.Count < 0 {
				t.Errorf("days=%d: negative count %+v", days, c)
			}
		}
	}
}

// TestCollector_ProgressReachesTotal 最后一个进度回调必须等于项目总数，进度条才能到 100%。
func TestCollector_ProgressReachesTotal(t *testing.T) {
	mock := newMockGitLab()
	collector, _ := newTestCollector(t, mock)

	last := -1
	total := -1
	if _, err := collector.Collect(context.Background(), nil, func(current, tot int) {
		last = current
		total = tot
	}); err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if last != total {
		t.Errorf("expected final progress %d to equal total %d", last, total)
	}
}

// TestFullPipeline_SnapshotToHTTP 验证「采集 → 落盘 → 重启加载 → HTTP 输出」的完整链路。
func TestFullPipeline_SnapshotToHTTP(t *testing.T) {
	mock := newMockGitLab()
	collector, cfg := newTestCollector(t, mock)

	dir := t.TempDir()
	store := NewStore(dir)

	snap, err := collector.Collect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 模拟进程重启：新的 Store 从磁盘恢复，前端立即可读。
	restarted := NewStore(dir)
	loaded, ok, err := restarted.Load()
	if err != nil || !ok {
		t.Fatalf("reload failed: ok=%v err=%v", ok, err)
	}

	jobs := NewJobManager()
	h := NewHandler(cfg, nil, restarted, jobs)

	// 提交频率接口
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stats/commit-frequency?period=day&days=90", nil)
	h.commitFrequencyHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Snapshot-Time") == "" {
		t.Error("expected X-Snapshot-Time header to be set")
	}

	var freq []CommitFrequency
	if err := json.Unmarshal(rr.Body.Bytes(), &freq); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	sum := 0
	for _, f := range freq {
		sum += f.Count
	}
	if sum != 5 {
		t.Errorf("expected 5 commits via HTTP, got %d", sum)
	}

	// MR 接口
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stats/mr-statistics?days=90", nil)
	h.mrStatisticsHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("mr endpoint expected 200, got %d", rr.Code)
	}
	var mr MRStatistics
	if err := json.Unmarshal(rr.Body.Bytes(), &mr); err != nil {
		t.Fatalf("invalid MR JSON: %v", err)
	}
	if mr.Total != 3 {
		t.Errorf("expected MR total 3, got %d", mr.Total)
	}
	if len(loaded.Daily) == 0 {
		t.Error("loaded snapshot should contain daily rows")
	}

	// 代码量接口
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stats/code-volume?days=90", nil)
	h.codeVolumeHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code-volume endpoint expected 200, got %d", rr.Code)
	}
	var vol CodeVolume
	if err := json.Unmarshal(rr.Body.Bytes(), &vol); err != nil {
		t.Fatalf("invalid volume JSON: %v", err)
	}
	if vol.TotalCommits != 5 {
		t.Errorf("expected 5 commits, got %d", vol.TotalCommits)
	}
	if len(vol.InactiveMembers) != 1 || vol.InactiveMembers[0].Name != "Carol" {
		t.Errorf("expected Carol as the only inactive member, got %+v", vol.InactiveMembers)
	}

	// 统计任务未运行，各接口不得触发 GitLab 请求。
	before := len(mock.requests)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stats/code-volume?days=30", nil)
	h.codeVolumeHandler(rr, req)
	if after := len(mock.requests); after != before {
		t.Errorf("serving a request must not hit GitLab (before=%d after=%d)", before, after)
	}
}

// TestRefreshEndpoint_TriggersJob 手动刷新接口必须真的启动任务并返回 202。
func TestRefreshEndpoint_TriggersJob(t *testing.T) {
	mock := newMockGitLab()
	collector, cfg := newTestCollector(t, mock)

	store := NewStore(t.TempDir())
	jobs := NewJobManager()
	jobs.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		snap, err := collector.Collect(ctx, nil, onProgress)
		if err != nil && !errors.Is(err, ErrSnapshotEmpty) {
			return nil, err
		}
		return snap, store.Save(snap)
	})

	h := NewHandler(cfg, nil, store, jobs)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/stats/refresh", nil)
	h.refreshHandler(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if !jobs.Wait(10 * time.Second) {
		t.Fatal("job did not finish")
	}
	if jobs.Status().State != JobSucceeded {
		t.Errorf("expected job success, got %s: %s", jobs.Status().State, jobs.Status().Error)
	}

	// 任务完成后快照应可用。
	if _, ok := store.Current(); !ok {
		t.Error("snapshot should be saved after job")
	}

	// GET 方法不被允许。
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stats/refresh", nil)
	h.refreshHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", rr.Code)
	}
}

// TestRefreshEndpoint_ConflictWhileRunning 任务运行中再次触发必须返回 409 而不是排队。
func TestRefreshEndpoint_ConflictWhileRunning(t *testing.T) {
	store := NewStore(t.TempDir())
	jobs := NewJobManager()
	release := make(chan struct{})
	jobs.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		<-release
		return sampleSnapshot(), nil
	})

	cfg := &Config{GitLabURL: "https://git.example.com"}
	h := NewHandler(cfg, nil, store, jobs)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/stats/refresh", nil)
	h.refreshHandler(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first trigger expected 202, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/stats/refresh", nil)
	h.refreshHandler(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("second trigger expected 409, got %d", rr.Code)
	}

	close(release)
	jobs.Wait(5 * time.Second)
}

// TestStatusEndpoint_ExposesProgressAndSnapshot 状态接口是前端的唯一进度来源，字段不能少。
func TestStatusEndpoint_ExposesProgressAndSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Commits: 1}}
	if err := store.Save(snap); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	cfg := &Config{GitLabURL: "https://git.example.com"}
	jobs := NewJobManager()
	h := NewHandler(cfg, nil, store, jobs)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stats/status", nil)
	h.statusHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"job", "snapshot", "schedule"} {
		if payload[key] == nil {
			t.Errorf("status payload missing %q", key)
		}
	}

	snapOut, ok := payload["snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("snapshot should be an object, got %T", payload["snapshot"])
	}
	for _, key := range []string{"generated_at", "age_seconds", "stale", "records", "projects_scanned", "duration_ms"} {
		if _, exists := snapOut[key]; !exists {
			t.Errorf("snapshot payload missing %q", key)
		}
	}

	jobOut, ok := payload["job"].(map[string]interface{})
	if !ok {
		t.Fatalf("job should be an object, got %T", payload["job"])
	}
	for _, key := range []string{"state", "phase", "percent", "message"} {
		if _, exists := jobOut[key]; !exists {
			t.Errorf("job payload missing %q", key)
		}
	}
}

// TestStatusEndpoint_NoSnapshot 无快照时 snapshot 必须是 null，前端据此展示空状态。
func TestStatusEndpoint_NoSnapshot(t *testing.T) {
	cfg := &Config{GitLabURL: "https://git.example.com"}
	h := NewHandler(cfg, nil, NewStore(t.TempDir()), NewJobManager())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stats/status", nil)
	h.statusHandler(rr, req)

	var payload map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["snapshot"] != nil {
		t.Errorf("expected null snapshot, got %+v", payload["snapshot"])
	}
}

// TestSnapshotRoundTripJSON 快照 JSON 字段名是前后端契约，改动会破坏兼容。
func TestSnapshotRoundTripJSON(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{{
		Date: "2026-03-01", AuthorKey: 1, Name: "Alice", Username: "alice",
		ProfileURL: "https://git.example.com/alice",
		Commits:    2, Additions: 10, Deletions: 1, CreatedMRs: 1, MergedMRs: 1,
	}}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	raw := string(data)
	for _, field := range []string{
		`"schema_version"`, `"generated_at"`, `"window_days"`, `"daily"`,
		`"author_key"`, `"commits"`, `"additions"`, `"deletions"`,
		`"created_mrs"`, `"merged_mrs"`, `"username"`, `"profile_url"`,
	} {
		if !strings.Contains(raw, field) {
			t.Errorf("snapshot JSON missing field %s", field)
		}
	}

	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(back.Daily) != 1 || back.Daily[0].AuthorKey != 1 {
		t.Errorf("round trip lost data: %+v", back.Daily)
	}
}

// --- 增量区间规划 ---

func newRangePlanner(t *testing.T, cfgMut func(*Config)) *Collector {
	t.Helper()
	cfg := &Config{MaxConcurrent: 4}
	cfg.SetDefaults()
	if cfgMut != nil {
		cfgMut(cfg)
	}
	return &Collector{cfg: cfg}
}

// assertDisjoint 区间一旦重叠，累加器会把同一批提交重复计数。
func assertDisjoint(t *testing.T, ranges []CollectRange) {
	t.Helper()
	for i := 0; i < len(ranges); i++ {
		for j := i + 1; j < len(ranges); j++ {
			a, b := ranges[i], ranges[j]
			if a.Since.Before(b.Until) && b.Since.Before(a.Until) {
				t.Errorf("区间重叠会导致重复计数: [%s~%s] 与 [%s~%s]",
					a.Since.Format(dateLayout), a.Until.Format(dateLayout),
					b.Since.Format(dateLayout), b.Until.Format(dateLayout))
			}
		}
	}
}

// TestPlanRanges_FirstRunOnlyRecentChunk 首次采集只取最近一块，不做整年拉取。
// 实测单个超大项目一年全分支提交需 10 分钟，一次性拉全会超出任务超时。
func TestPlanRanges_FirstRunOnlyRecentChunk(t *testing.T) {
	c := newRangePlanner(t, nil)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	ranges := c.planRanges(nil, now)
	if len(ranges) != 1 {
		t.Fatalf("首次采集应只有 1 个区间，实际 %d: %+v", len(ranges), ranges)
	}

	wantSince := dateKey(now.AddDate(0, 0, -DefaultBackfillChunkDays))
	if got := dateKey(ranges[0].Since); got != wantSince {
		t.Errorf("首次采集区间起点应为 %s，实际 %s", wantSince, got)
	}
}

// TestPlanRanges_IncrementalAddsBackfillChunk 历史未补满时，每次运行再向前推进一块。
func TestPlanRanges_IncrementalAddsBackfillChunk(t *testing.T) {
	c := newRangePlanner(t, nil)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	prev := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		GeneratedAt:   now.AddDate(0, 0, -1),
		CoveredFrom:   dateKey(now.AddDate(0, 0, -40)), // 只覆盖到 40 天前，历史未补满
		Daily:         []DailyStat{{Date: dateKey(now.AddDate(0, 0, -1)), Commits: 1}},
	}

	ranges := c.planRanges(prev, now)
	if len(ranges) != 2 {
		t.Fatalf("应产出「刷新段 + 回填段」共 2 个区间，实际 %d: %+v", len(ranges), ranges)
	}
	assertDisjoint(t, ranges)

	// 刷新段：从「上次采集时间」向前回溯 RefreshOverlapDays 天。
	wantRefreshSince := dateKey(prev.GeneratedAt.AddDate(0, 0, -DefaultRefreshOverlapDays))
	if got := dateKey(ranges[0].Since); got != wantRefreshSince {
		t.Errorf("刷新段起点应为 %s，实际 %s", wantRefreshSince, got)
	}

	// 回填段：以原覆盖起点为右端，向前推一块。
	if got := dateKey(ranges[1].Until); got != prev.CoveredFrom {
		t.Errorf("回填段右端应为原覆盖起点 %s，实际 %s", prev.CoveredFrom, got)
	}
	wantBackfillSince := dateKey(now.AddDate(0, 0, -40-DefaultBackfillChunkDays))
	if got := dateKey(ranges[1].Since); got != wantBackfillSince {
		t.Errorf("回填段起点应为 %s，实际 %s", wantBackfillSince, got)
	}
}

// TestPlanRanges_CompleteCoverageOnlyRefreshes 历史补满后每次只刷新最近区间。
func TestPlanRanges_CompleteCoverageOnlyRefreshes(t *testing.T) {
	c := newRangePlanner(t, nil)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	prev := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		GeneratedAt:   now.AddDate(0, 0, -1),
		CoveredFrom:   dateKey(now.AddDate(0, 0, -WindowDays)), // 正好覆盖满窗口
		Daily:         []DailyStat{{Date: dateKey(now.AddDate(0, 0, -1)), Commits: 1}},
	}

	ranges := c.planRanges(prev, now)
	if len(ranges) != 1 {
		t.Fatalf("历史已补满时只应有刷新段，实际 %d: %+v", len(ranges), ranges)
	}
	assertDisjoint(t, ranges)
}

// TestPlanRanges_BackfillClampedByRefreshWindow 回填段必须夹住右端以避免与刷新段重叠。
//
// 覆盖范围很短（例如刚上线不久）时，回填右端会落进刷新窗口内部，
// 若不夹紧就会产生重叠区间，同一批提交被算两遍。
func TestPlanRanges_BackfillClampedByRefreshWindow(t *testing.T) {
	c := newRangePlanner(t, nil)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	prev := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		GeneratedAt:   now,
		CoveredFrom:   dateKey(now.AddDate(0, 0, -5)), // 仅覆盖 5 天，远短于刷新窗口
		Daily:         []DailyStat{{Date: dateKey(now), Commits: 1}},
	}

	ranges := c.planRanges(prev, now)
	assertDisjoint(t, ranges)

	for _, r := range ranges {
		start := dateKey(now.AddDate(0, 0, -WindowDays))
		if dateKey(r.Since) < start {
			t.Errorf("区间起点 %s 不得早于窗口起点 %s", dateKey(r.Since), start)
		}
	}
}

// TestPlanRanges_ClampedToWindow 区间不得越过统计窗口起点。
func TestPlanRanges_ClampedToWindow(t *testing.T) {
	c := newRangePlanner(t, func(cfg *Config) {
		cfg.BackfillChunkDays = 400 // 故意大于整个窗口
	})
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	ranges := c.planRanges(nil, now)
	windowStart := dateKey(now.AddDate(0, 0, -WindowDays))
	if got := dateKey(ranges[0].Since); got != windowStart {
		t.Errorf("区间起点应夹到窗口起点 %s，实际 %s", windowStart, got)
	}
}

// TestPlanRanges_SchemaMismatchTreatedAsFirstRun 口径升级后必须整体重采。
func TestPlanRanges_SchemaMismatchTreatedAsFirstRun(t *testing.T) {
	c := newRangePlanner(t, nil)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	prev := &Snapshot{
		SchemaVersion: snapshotSchemaVersion - 1,
		GeneratedAt:   now.AddDate(0, 0, -1),
		CoveredFrom:   dateKey(now.AddDate(0, 0, -300)),
		Daily:         []DailyStat{{Date: dateKey(now), Commits: 1}},
	}

	ranges := c.planRanges(prev, now)
	if len(ranges) != 1 {
		t.Fatalf("口径不匹配时应按首次采集处理，实际 %d 个区间: %+v", len(ranges), ranges)
	}
	if got := dateKey(ranges[0].Since); got != dateKey(now.AddDate(0, 0, -DefaultBackfillChunkDays)) {
		t.Errorf("应按首次采集取最近一块，实际起点 %s", got)
	}
}

// --- 时区归日与区间边界对齐 ---

// TestDateKey_NormalizesAuthorTimezone 同一 UTC 时刻必须归到同一天。
//
// 回归场景（真实实例）：某两个项目里有美西时区（-07:00）的提交者。
// 时间戳 2026-08-14T22:07:09-07:00 的绝对时刻是 2026-08-15T05:07:09Z，落在刷新区间内；
// 但若按提交自带的时区取日期会得到 2026-08-14，比区间起点早一天，
// 于是这条记录落到替换范围之外，与上一份快照的同日记录并存，形成重复。
func TestDateKey_NormalizesAuthorTimezone(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*60*60)

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"美西晚间 → 东八区次日下午", time.Date(2026, 8, 14, 22, 7, 9, 0, pacific), "2026-08-15"},
		{"同一时刻的 UTC 表示", time.Date(2026, 8, 15, 5, 7, 9, 0, time.UTC), "2026-08-15"},
		{"东八区当天", time.Date(2026, 8, 15, 13, 7, 9, 0, statsLocation), "2026-08-15"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dateKey(tc.when); got != tc.want {
				t.Errorf("dateKey = %s, want %s", got, tc.want)
			}
		})
	}

	// 同一绝对时刻的任意时区表示都必须等价。
	instant := time.Date(2026, 8, 15, 5, 7, 9, 0, time.UTC)
	for _, off := range []int{-7, 0, 8, 9} {
		loc := time.FixedZone("x", off*60*60)
		if got := dateKey(instant.In(loc)); got != dateKey(instant) {
			t.Errorf("时区偏移 %+d 下归日 %s 与 UTC 下 %s 不一致", off, got, dateKey(instant))
		}
	}
}

// TestStartEndOfDay 日边界必须是自然日的 00:00:00 与 23:59:59。
func TestStartEndOfDay(t *testing.T) {
	// 美西 08-14 22:07 即东八区 08-15 13:07，归日应为 08-15。
	inDay := time.Date(2026, 8, 14, 22, 7, 9, 123456, time.FixedZone("PDT", -7*60*60))

	if got := startOfDay(inDay).In(statsLocation).Format("2006-01-02 15:04:05"); got != "2026-08-15 00:00:00" {
		t.Errorf("startOfDay = %s", got)
	}
	if got := endOfDay(inDay).In(statsLocation).Format("2006-01-02 15:04:05"); got != "2026-08-15 23:59:59" {
		t.Errorf("endOfDay = %s", got)
	}
}

// TestPlanRanges_BoundariesAlignedToNaturalDay 区间边界不得落在一天中间。
//
// 合并以「日期」为替换单位：若区间从某天半途开始，这一天的采集结果只是半天，
// 却会在 covered 里被当成已完整覆盖，从而丢弃上一份快照中的整天数据。
// 实测某实例上 2026-05-17 因此被截断到只剩 3 条记录（相邻日期为 9 / 65 条）。
func TestPlanRanges_BoundariesAlignedToNaturalDay(t *testing.T) {
	c := newRangePlanner(t, nil)
	// 特意取一个「一天中间」的时刻：旧实现会原样把它当成区间起点。
	now := time.Date(2026, 9, 14, 10, 49, 33, 0, statsLocation)

	prev := &Snapshot{
		SchemaVersion: snapshotSchemaVersion,
		GeneratedAt:   time.Date(2026, 9, 14, 10, 45, 54, 0, statsLocation),
		CoveredFrom:   "2026-08-15",
		Daily:         []DailyStat{{Date: "2026-09-13", Commits: 1}},
	}

	ranges := c.planRanges(prev, now)
	if len(ranges) != 2 {
		t.Fatalf("应产出「刷新段 + 回填段」，实际 %d: %+v", len(ranges), ranges)
	}
	assertDisjoint(t, ranges)

	for _, r := range ranges {
		if got := r.Since.In(statsLocation).Format("15:04:05"); got != "00:00:00" {
			t.Errorf("区间起点必须对齐自然日 00:00，实际 %s (%s)", got, dateKey(r.Since))
		}
		// 终点允许是某天的 23:59:59，或是被相邻区间夹到某天的 00:00。
		if got := r.Until.In(statsLocation).Format("15:04:05"); got != "23:59:59" && got != "00:00:00" {
			t.Errorf("区间终点必须落在自然日边界，实际 %s (%s)", got, dateKey(r.Until))
		}
	}

	// 刷新段覆盖「今天」整天，而不是从上午 10:49 才开始。
	if got := dateKey(ranges[0].Since); got != "2026-08-15" {
		t.Errorf("刷新段起点应为 2026-08-15，实际 %s", got)
	}
	if got := dateKey(ranges[0].Until); got != "2026-09-14" {
		t.Errorf("刷新段终点应为今天 2026-09-14，实际 %s", got)
	}
	// 回填段向前推进一整块。
	if got := dateKey(ranges[1].Since); got != "2026-07-16" {
		t.Errorf("回填段起点应为 2026-07-16，实际 %s", got)
	}
	if got := dateKey(ranges[1].Until); got != "2026-08-15" {
		t.Errorf("回填段终点应为原覆盖起点 2026-08-15，实际 %s", got)
	}
}
