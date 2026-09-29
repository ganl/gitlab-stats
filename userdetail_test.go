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
