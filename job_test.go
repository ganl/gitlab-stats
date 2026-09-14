package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestParseCron_Valid(t *testing.T) {
	tests := []struct {
		expr string
	}{
		{"0 2 * * *"},        // 每天 02:00
		{"*/15 * * * *"},     // 每 15 分钟
		{"0 */6 * * *"},      // 每 6 小时
		{"30 1 * * 1-5"},     // 工作日 01:30
		{"0 0 1 * *"},        // 每月 1 日
		{"0 9,18 * * 1,3,5"}, // 周一三五 09:00 与 18:00
		{"0 2 * * 0"},        // 每周日
	}

	for _, tt := range tests {
		if _, err := ParseCron(tt.expr); err != nil {
			t.Errorf("ParseCron(%q) unexpected error: %v", tt.expr, err)
		}
	}
}

func TestParseCron_Invalid(t *testing.T) {
	tests := []struct {
		expr string
		desc string
	}{
		{"", "空表达式"},
		{"0 2 * *", "字段数不足"},
		{"0 2 * * * *", "字段数过多"},
		{"60 2 * * *", "分钟越界"},
		{"0 24 * * *", "小时越界"},
		{"0 2 32 * *", "日期越界"},
		{"0 2 * 13 *", "月份越界"},
		{"0 2 * * 8", "星期越界"},
		{"abc * * * *", "非数值"},
		{"*/0 * * * *", "步进为 0"},
		{"5-1 * * * *", "区间反序"},
	}

	for _, tt := range tests {
		if _, err := ParseCron(tt.expr); err == nil {
			t.Errorf("ParseCron(%q) should fail (%s)", tt.expr, tt.desc)
		}
	}
}

// TestCron_NextDaily 每日 02:00 的下一次触发必须落在次日或当日凌晨。
func TestCron_NextDaily(t *testing.T) {
	c, err := ParseCron("0 2 * * *")
	if err != nil {
		t.Fatalf("ParseCron failed: %v", err)
	}

	base := time.Date(2026, 3, 10, 10, 30, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected a next run time")
	}
	want := time.Date(2026, 3, 11, 2, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("expected %v, got %v", want, next)
	}
}

// TestCron_NextSameDay 凌晨 01:00 查询时，当天 02:00 仍应被选中。
func TestCron_NextSameDay(t *testing.T) {
	c, _ := ParseCron("0 2 * * *")

	base := time.Date(2026, 3, 10, 1, 0, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected a next run time")
	}
	want := time.Date(2026, 3, 10, 2, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("expected same-day %v, got %v", want, next)
	}
}

func TestCron_NextEvery15Minutes(t *testing.T) {
	c, _ := ParseCron("*/15 * * * *")

	base := time.Date(2026, 3, 10, 10, 7, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected a next run time")
	}
	if next.Minute()%15 != 0 {
		t.Errorf("expected minute divisible by 15, got %d", next.Minute())
	}
	want := time.Date(2026, 3, 10, 10, 15, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("expected %v, got %v", want, next)
	}
}

// TestCron_NextWeekdayOnly 周末必须被跳过。
func TestCron_NextWeekdayOnly(t *testing.T) {
	c, _ := ParseCron("30 1 * * 1-5")

	// 2026-03-14 是周六。
	base := time.Date(2026, 3, 14, 5, 0, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected a next run time")
	}
	if wd := next.Weekday(); wd == time.Saturday || wd == time.Sunday {
		t.Errorf("expected a weekday, got %v (%v)", wd, next)
	}
	want := time.Date(2026, 3, 16, 1, 30, 0, 0, time.Local) // 下周一
	if !next.Equal(want) {
		t.Errorf("expected %v, got %v", want, next)
	}
}

// TestCron_NextMonthlyFirstDay 每月 1 日应跳到下月。
func TestCron_NextMonthlyFirstDay(t *testing.T) {
	c, _ := ParseCron("0 0 1 * *")

	base := time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected a next run time")
	}
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("expected %v, got %v", want, next)
	}
}

// TestCron_String 表达式还原必须与输入语义一致，便于日志核对。
func TestCron_String(t *testing.T) {
	expr := "0 9,18 * * 1,3,5"
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron failed: %v", err)
	}
	if got := c.String(); got != expr {
		t.Errorf("expected %q, got %q", expr, got)
	}
}

// TestCron_WildcardDaySemantics 日与周同时通配时任意日期都匹配。
func TestCron_WildcardDaySemantics(t *testing.T) {
	c, _ := ParseCron("0 2 * * *")
	for i := 0; i < 14; i++ {
		day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local).AddDate(0, 0, i)
		if !c.matchDay(day) {
			t.Errorf("wildcard dom/dow should match %v", day)
		}
	}
}

// --- JobManager ---

func TestJobManager_TriggerSucceedsAndReportsProgress(t *testing.T) {
	m := NewJobManager()
	progressed := make(chan int, 4)

	m.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		for i := 1; i <= 3; i++ {
			onProgress(i, 3)
			progressed <- i
		}
		return sampleSnapshot(), nil
	})

	status, err := m.Trigger("test")
	if err != nil {
		t.Fatalf("Trigger failed: %v", err)
	}
	if status.State != JobRunning {
		t.Errorf("expected running state, got %s", status.State)
	}

	if !m.Wait(5 * time.Second) {
		t.Fatal("job did not finish in time")
	}

	final := m.Status()
	if final.State != JobSucceeded {
		t.Errorf("expected succeeded, got %s (%s)", final.State, final.Error)
	}
	if final.Percent != 100 {
		t.Errorf("expected 100%%, got %.1f", final.Percent)
	}
	if final.Total != 3 {
		t.Errorf("expected total 3, got %d", final.Total)
	}
	close(progressed)

	if m.Running() {
		t.Error("job should not be running after completion")
	}
}

func TestJobManager_RejectsConcurrentTrigger(t *testing.T) {
	m := NewJobManager()
	release := make(chan struct{})

	m.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		<-release
		return sampleSnapshot(), nil
	})

	if _, err := m.Trigger("first"); err != nil {
		t.Fatalf("first trigger failed: %v", err)
	}

	_, err := m.Trigger("second")
	if !errors.Is(err, ErrJobRunning) {
		t.Errorf("expected ErrJobRunning, got %v", err)
	}

	close(release)
	if !m.Wait(5 * time.Second) {
		t.Fatal("job did not finish")
	}
}

func TestJobManager_FailureIsCaptured(t *testing.T) {
	m := NewJobManager()
	m.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		return nil, errors.New("gitlab unreachable")
	})

	if _, err := m.Trigger("test"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}
	if !m.Wait(5 * time.Second) {
		t.Fatal("job did not finish")
	}

	final := m.Status()
	if final.State != JobFailed {
		t.Errorf("expected failed state, got %s", final.State)
	}
	if final.Error == "" {
		t.Error("expected error message to be captured")
	}
}

// TestJobManager_FailedProjectList 单项目失败必须上报给前端而不是被吞掉。
func TestJobManager_FailedProjectList(t *testing.T) {
	m := NewJobManager()
	m.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		snap := sampleSnapshot()
		snap.Totals.ProjectErrors = []string{"broken-repo: 403 Forbidden"}
		return snap, nil
	})

	if _, err := m.Trigger("test"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}
	m.Wait(5 * time.Second)

	final := m.Status()
	if final.Failed != 1 {
		t.Errorf("expected 1 failed project, got %d", final.Failed)
	}
	if len(final.FailedList) != 1 {
		t.Errorf("expected failed list to be exposed, got %+v", final.FailedList)
	}
}

func TestJobManager_NoCollectorIsAnError(t *testing.T) {
	m := NewJobManager()
	if _, err := m.Trigger("test"); err == nil {
		t.Error("expected error when collector is not configured")
	}
}

// TestJobManager_EmptyRetryThrottle 空结果重试必须节流，避免 GitLab 宕机时打满定时轮次。
func TestJobManager_EmptyRetryThrottle(t *testing.T) {
	m := NewJobManager()

	// 首次尝试前 lastAttempt 为零值，不应被误判为「刚试过」。
	if !m.EmptyRetryAllowed(3, time.Hour) {
		t.Error("first retry should be allowed")
	}

	// 模拟一次真实尝试：Trigger 会记录 lastAttempt 与当前任务。
	release := make(chan struct{})
	m.SetCollector(func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error) {
		<-release
		return sampleSnapshot(), nil
	})
	if _, err := m.Trigger("test"); err != nil {
		t.Fatalf("trigger failed: %v", err)
	}

	// 任务执行期间不允许再次触发。
	if m.EmptyRetryAllowed(3, 0) {
		t.Error("retry during a running job should be rejected")
	}

	close(release)
	if !m.Wait(5 * time.Second) {
		t.Fatal("job did not finish")
	}
	m.RecordEmptyResult()

	// 距上次尝试过近，应被拒绝。
	if m.EmptyRetryAllowed(3, time.Hour) {
		t.Error("retry too soon should be rejected")
	}

	time.Sleep(15 * time.Millisecond)
	// 节流窗口过后允许。
	if !m.EmptyRetryAllowed(3, 10*time.Millisecond) {
		t.Error("retry after interval should be allowed")
	}

	m.RecordEmptyResult()
	m.RecordEmptyResult()
	m.RecordEmptyResult()
	if m.EmptyRetryAllowed(3, 0) {
		t.Error("retry beyond max attempts should be rejected")
	}

	m.ResetEmptyRetries()
	if m.EmptyRetries() != 0 {
		t.Errorf("expected counter reset, got %d", m.EmptyRetries())
	}
	if !m.EmptyRetryAllowed(3, 0) {
		t.Error("retry should be allowed after reset")
	}
}

// TestComputePercent_RunningIsCapped 采集阶段的进度不得超过 95%，给落盘留出余量。
func TestComputePercent_RunningIsCapped(t *testing.T) {
	st := JobStatus{State: JobRunning, Phase: PhaseCollect, Current: 100, Total: 100}
	if got := computePercent(st); got > 95 {
		t.Errorf("collection progress should be capped at 95, got %.1f", got)
	}

	st.Phase = PhasePersist
	if got := computePercent(st); got != 97 {
		t.Errorf("persist phase should report 97, got %.1f", got)
	}

	st = JobStatus{State: JobRunning, Phase: PhaseCollect, Current: 0, Total: 0}
	if got := computePercent(st); got <= 0 {
		t.Errorf("expected positive progress before totals are known, got %.1f", got)
	}
}

// TestJobStatus_JSONShape 前端依赖这些字段名，改动会破坏页面。
func TestJobStatus_JSONShape(t *testing.T) {
	st := JobStatus{
		State:   JobRunning,
		Phase:   PhaseCollect,
		Message: "正在从 GitLab 采集数据...",
		Current: 3,
		Total:   10,
	}
	if st.State != "running" || st.Phase != "collecting" {
		t.Errorf("unexpected enum values: %s / %s", st.State, st.Phase)
	}
	if strconv.Itoa(st.Current) != "3" {
		t.Error("unexpected current value")
	}
}
