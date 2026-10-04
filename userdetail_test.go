package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------- 个人贡献下钻接口 ----------

// userDetailTestHandler 构造一个带两名贡献者（分属不同 AuthorKey）的处理器。
func userDetailTestHandler(t *testing.T) *Handler {
	t.Helper()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5, Additions: 100, Deletions: 20},
		{Date: mkDate(1), AuthorKey: 2, Name: "Bob", Username: "bob", Commits: 3},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	return &Handler{store: store, jobs: NewJobManager(), cfg: &Config{}}
}

func decodeUserDetail(t *testing.T, rr *httptest.ResponseRecorder) UserDetail {
	t.Helper()

	var detail UserDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("响应不是合法的 UserDetail: %v", err)
	}
	return detail
}

// TestUserDetailHandler_ByUsername 按用户名下钻应只汇总该人记录。
func TestUserDetailHandler_ByUsername(t *testing.T) {
	handler := userDetailTestHandler(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=alice&days=90", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	detail := decodeUserDetail(t, rr)
	if detail.Totals.Commits != 5 {
		t.Errorf("commits = %d, want 5", detail.Totals.Commits)
	}
	if detail.Totals.Additions != 100 {
		t.Errorf("additions = %d, want 100", detail.Totals.Additions)
	}
}

// TestUserDetailHandler_NotFound 查无此人必须回 404，而非凭空的数字。
func TestUserDetailHandler_NotFound(t *testing.T) {
	handler := userDetailTestHandler(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=ghost&days=90", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// TestUserDetailHandler_MissingParam 缺 user 参数必须回 400，不能静默当作「全部」。
func TestUserDetailHandler_MissingParam(t *testing.T) {
	handler := userDetailTestHandler(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?days=90", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// userDetailTestHandler_Stale 构造「窗口外仍有历史贡献」的场景：
// alice 今天活跃，bob 只在 40 天前提交过，ghost 是未匹配到账号的镜像身份（AuthorKey=0）且 50 天前活跃。
func userDetailTestHandler_Stale(t *testing.T) *Handler {
	t.Helper()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(0), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
		{Date: mkDate(40), AuthorKey: 2, Name: "Bob", Username: "bob", Commits: 3, Additions: 40, Deletions: 10},
		{Date: mkDate(50), AuthorKey: 0, Name: "Ghost", Username: "ghost", Commits: 2},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	return &Handler{store: store, jobs: NewJobManager(), cfg: &Config{}}
}

// TestUserDetailHandler_NoActivityInWindow 成员存在、只是展示窗口内没有活动时，
// 必须返回 200 空结果并标记 empty。此前把「窗口内无记录」与「查无此人」混判，
// 一律回 404，导致切到「最近 7 天」时窗口外有贡献的成员被整批误判为不存在。
func TestUserDetailHandler_NoActivityInWindow(t *testing.T) {
	handler := userDetailTestHandler_Stale(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=bob&days=7", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200（人存在，只是窗口内无活动）, got %d", rr.Code)
	}
	detail := decodeUserDetail(t, rr)
	if !detail.Empty {
		t.Errorf("empty = false, want true：窗口内无活动时应如实标记空结果")
	}
	if len(detail.Daily) != 0 {
		t.Errorf("daily 条数 = %d, want 0（40 天前不在 7 天窗口内）", len(detail.Daily))
	}
	// 身份展示信息要靠历史日报补齐，否则前端会显示成空白名字。
	if detail.User.Username != "bob" {
		t.Errorf("username = %q, want %q（应自历史日报补齐）", detail.User.Username, "bob")
	}
}

// TestUserDetailHandler_EmptyForUnmatchedIdentity 未匹配账号（AuthorKey=0）同样适用上述口径：
// 不能因为它不在用户表里就报 404。
func TestUserDetailHandler_EmptyForUnmatchedIdentity(t *testing.T) {
	handler := userDetailTestHandler_Stale(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=ghost&days=7", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d（镜像身份不应因不在用户表而被判 404）", rr.Code)
	}
	detail := decodeUserDetail(t, rr)
	if !detail.Empty {
		t.Errorf("empty = false, want true")
	}
	if detail.User.Name != "Ghost" {
		t.Errorf("name = %q, want %q（应自历史日报补齐）", detail.User.Name, "Ghost")
	}
}

// TestUserDetailHandler_StaleActivityReflected 同一成员放大窗口后必须读到那条历史记录，
// 确认 empty 只是「窗口裁掉了」，不是把人整个丢掉。
func TestUserDetailHandler_StaleActivityReflected(t *testing.T) {
	handler := userDetailTestHandler_Stale(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=bob&days=90", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	detail := decodeUserDetail(t, rr)
	if detail.Empty {
		t.Errorf("empty = true, want false：90 天窗口内确实有活动")
	}
	if detail.Totals.Commits != 3 {
		t.Errorf("commits = %d, want 3", detail.Totals.Commits)
	}
}

// userDetailTestHandler_NoRecord 构造「在用户表里但整张快照都没有活动记录」的成员（dana），
// 这类身份正是只选入选择器、却因不承担编码指标而没有任何提交的人。
func userDetailTestHandler_NoRecord(t *testing.T) *Handler {
	t.Helper()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
	}
	dana := SnapshotUser{ID: 7, Name: "Dana", Username: "dana", Email: "dana@example.com"}
	snap.Totals.Users = append(snap.Totals.Users, dana)
	if err := store.Save(snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	return &Handler{store: store, jobs: NewJobManager(), cfg: &Config{}}
}

// TestUserDetailHandler_ExistsButNoRecordAtAll 成员确实存在于用户表，只是快照里一条记录都没有：
// 这属于「人存在、无贡献」，必须返回 200 空结果，而不是 404 —— 否则从选择器选中就会直接报错。
func TestUserDetailHandler_ExistsButNoRecordAtAll(t *testing.T) {
	handler := userDetailTestHandler_NoRecord(t)
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/user-detail?user=dana&days=90", nil)

	handler.userDetailHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200（成员在用户表中）, got %d", rr.Code)
	}
	detail := decodeUserDetail(t, rr)
	if !detail.Empty {
		t.Errorf("empty = false, want true：人存在但无任何记录，应如实标空结果")
	}
	if detail.User.Username != "dana" {
		t.Errorf("username = %q, want %q（用户表身份应原样返回）", detail.User.Username, "dana")
	}
}

// TestUsersHandler_FiltersBotsAndExcluded 选择器列表必须剔除 bot 与被排除账号。
func TestUsersHandler_FiltersBotsAndExcluded(t *testing.T) {
	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.ExcludedAuthors = []string{"carol"}
	snap.Totals.Users = append(snap.Totals.Users, SnapshotUser{
		ID: 99, Name: "Bot", Username: "ci-bot", Bot: true,
	})
	// 快照必须含日报记录，否则 requireSnapshot 会按「无数据」回 503。
	snap.Daily = []DailyStat{{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 1}}
	if err := store.Save(snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	handler := &Handler{store: store, jobs: NewJobManager(), cfg: &Config{}}

	rr := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/stats/users", nil)

	handler.usersHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var list []UserIdentity
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是合法的用户列表: %v", err)
	}

	// 4 名普通成员 - 1 名被排除 (carol) = 3，bot 不计入。
	if len(list) != 3 {
		t.Fatalf("用户列表长度 = %d, want 3 (bot 与排除账号已剔除)", len(list))
	}
	for _, u := range list {
		if u.Username == "ci-bot" || u.Username == "carol" {
			t.Errorf("用户列表不应包含 %q", u.Username)
		}
	}
}
