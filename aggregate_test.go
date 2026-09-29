package main

import (
	"testing"
)

// TestBuildUserDetail_ByUsername 按用户名下钻，应只汇总该人记录，不混入他人。
func TestBuildUserDetail_ByUsername(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5, Additions: 100, Deletions: 20, CreatedMRs: 2, MergedMRs: 1},
		{Date: mkDate(2), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 3, Additions: 40, Deletions: 10, MergedMRs: 2},
		{Date: mkDate(1), AuthorKey: 2, Name: "Bob", Username: "bob", Commits: 99},
	}

	d, err := BuildUserDetail(snap, "alice", "day", 90, "https://git.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.Totals.Commits != 8 {
		t.Errorf("commits = %d, want 8", d.Totals.Commits)
	}
	if d.Totals.Additions != 140 {
		t.Errorf("additions = %d, want 140", d.Totals.Additions)
	}
	if d.Totals.Deletions != 30 {
		t.Errorf("deletions = %d, want 30", d.Totals.Deletions)
	}
	if d.MR.Created != 2 || d.MR.Merged != 3 {
		t.Errorf("mr = %+v, want created=2 merged=3", d.MR)
	}
	if len(d.Daily) != 2 {
		t.Errorf("daily rows = %d, want 2", len(d.Daily))
	}
	// 每日明细之和应与总量一致。
	var sumCommits int
	for _, day := range d.Daily {
		sumCommits += day.Commits
	}
	if sumCommits != d.Totals.Commits {
		t.Errorf("daily commit sum = %d, want %d", sumCommits, d.Totals.Commits)
	}
}

// TestBuildUserDetail_ByID 数字查询应走 AuthorKey 精确匹配。
func TestBuildUserDetail_ByID(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
		{Date: mkDate(1), AuthorKey: 2, Name: "Bob", Username: "bob", Commits: 7},
	}

	d, err := BuildUserDetail(snap, "1", "day", 90, "https://git.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Totals.Commits != 5 {
		t.Errorf("commits = %d, want 5 (matched by ID)", d.Totals.Commits)
	}
	if d.User.Username != "alice" {
		t.Errorf("username = %q, want alice", d.User.Username)
	}
}

// TestBuildUserDetail_Window 超出展示窗口的记录必须被裁剪。
func TestBuildUserDetail_Window(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
		{Date: mkDate(200), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 50},
	}

	d, err := BuildUserDetail(snap, "alice", "day", 90, "https://git.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Totals.Commits != 5 {
		t.Errorf("commits = %d, want 5 (old record outside 90-day window)", d.Totals.Commits)
	}
}

// TestBuildUserDetail_Excluded 被排除名单命中的账号返回空结果并明确标注。
func TestBuildUserDetail_Excluded(t *testing.T) {
	snap := sampleSnapshot()
	snap.ExcludedAuthors = []string{"alice"}
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
	}

	d, err := BuildUserDetail(snap, "alice", "day", 90, "https://git.example.com")
	if err != nil {
		t.Fatalf("excluded user should not error: %v", err)
	}
	if !d.Excluded {
		t.Error("expected Excluded=true for excluded author")
	}
	if d.Totals.Commits != 0 {
		t.Errorf("excluded user totals should be 0, got %d", d.Totals.Commits)
	}
}

// TestBuildUserDetail_NotFound 查无此人且无日报命中应报错（调用方据此回 404）。
func TestBuildUserDetail_NotFound(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5},
	}

	if _, err := BuildUserDetail(snap, "ghost", "day", 90, "https://git.example.com"); err == nil {
		t.Fatal("expected error for non-existent user")
	}
}

// TestBuildUserDetail_MissingParam 空查询应报错。
func TestBuildUserDetail_MissingParam(t *testing.T) {
	snap := sampleSnapshot()
	if _, err := BuildUserDetail(snap, "   ", "day", 90, "https://git.example.com"); err == nil {
		t.Fatal("expected error for empty user query")
	}
}

// TestBuildUserDetail_StatsIncomplete 任一日报标记行数不可信时，顶层标志应置位。
func TestBuildUserDetail_StatsIncomplete(t *testing.T) {
	snap := sampleSnapshot()
	snap.Daily = []DailyStat{
		{Date: mkDate(1), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 5, StatsIncomplete: true},
		{Date: mkDate(2), AuthorKey: 1, Name: "Alice", Username: "alice", Commits: 3},
	}

	d, err := BuildUserDetail(snap, "alice", "day", 90, "https://git.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.StatsIncomplete {
		t.Error("expected StatsIncomplete=true when any daily record is incomplete")
	}
}
