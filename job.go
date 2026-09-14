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
	"log"
	"sync"
	"time"
)

// ErrJobRunning 表示已有统计任务在执行，不允许并发触发。
var ErrJobRunning = errors.New("统计任务正在执行中")

// JobState 描述后台统计任务的状态机。
type JobState string

const (
	JobIdle      JobState = "idle"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
)

// JobPhase 是进度阶段的语义标签，供前端渲染可读进度。
type JobPhase string

const (
	PhaseIdle      JobPhase = "idle"
	PhaseCollect   JobPhase = "collecting"
	PhaseAggregate JobPhase = "aggregating"
	PhasePersist   JobPhase = "persisting"
	PhaseDone      JobPhase = "done"
)

// JobStatus 是暴露给前端的进度快照。每次读取都返回副本，避免数据竞争。
type JobStatus struct {
	State      JobState  `json:"state"`
	Phase      JobPhase  `json:"phase"`
	Message    string    `json:"message"`
	Current    int       `json:"current"`
	Total      int       `json:"total"`
	Percent    float64   `json:"percent"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	Failed     int       `json:"failed_projects,omitempty"`
	FailedList []string  `json:"failed_list,omitempty"`
	Trigger    string    `json:"trigger,omitempty"`
}

type job struct {
	mu     sync.RWMutex
	status JobStatus
	done   chan struct{}
}

// JobManager 串行化统计任务，并向 HTTP 层暴露进度。
type JobManager struct {
	mu         sync.Mutex
	current    *job
	lastStatus JobStatus

	// collector 由 main 注入，避免与 Handler 相互持有造成循环依赖。
	collector func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error)

	// 空快照重试节流：避免 GitLab 长时间不可用时每次定时都打满一轮请求。
	emptyRetries int
	lastAttempt  time.Time
}

func NewJobManager() *JobManager {
	return &JobManager{
		lastStatus: JobStatus{State: JobIdle, Phase: PhaseIdle, Message: "尚未执行统计任务"},
	}
}

func (m *JobManager) SetCollector(fn func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collector = fn
}

// Trigger 触发一次后台统计。已有任务在执行时返回 ErrJobRunning。
func (m *JobManager) Trigger(trigger string) (JobStatus, error) {
	m.mu.Lock()
	if m.current != nil {
		st := m.current.snapshot()
		m.mu.Unlock()
		return st, ErrJobRunning
	}

	collector := m.collector
	if collector == nil {
		m.mu.Unlock()
		return JobStatus{}, errors.New("统计采集器未初始化")
	}

	j := &job{done: make(chan struct{})}
	j.set(JobStatus{
		State:     JobRunning,
		Phase:     PhaseCollect,
		Message:   "正在从 GitLab 采集数据...",
		StartedAt: time.Now(),
		Trigger:   trigger,
	})
	m.current = j
	m.lastAttempt = time.Now()
	m.mu.Unlock()

	go m.run(j, collector)

	return j.snapshot(), nil
}

func (m *JobManager) run(j *job, collector func(ctx context.Context, onProgress func(current, total int)) (*Snapshot, error)) {
	started := time.Now()

	snap, err := collector(context.Background(), func(current, total int) {
		j.update(func(st *JobStatus) {
			st.Phase = PhaseCollect
			st.Current = current
			st.Total = total
			st.Message = "正在从 GitLab 采集数据..."
		})
	})

	if err == nil {
		j.update(func(st *JobStatus) {
			st.Phase = PhasePersist
			st.Message = "正在写入本地统计快照..."
			st.Current = st.Total
		})
	}

	finished := time.Now()

	j.update(func(st *JobStatus) {
		st.FinishedAt = finished
		st.DurationMS = finished.Sub(started).Milliseconds()
		if err != nil {
			st.State = JobFailed
			st.Phase = PhaseDone
			st.Message = "统计任务执行失败"
			st.Error = err.Error()
			return
		}
		st.State = JobSucceeded
		st.Phase = PhaseDone
		st.Current = st.Total
		st.Message = "统计完成，快照已更新"
		if snap != nil {
			st.Failed = len(snap.Totals.ProjectErrors)
			st.FailedList = snap.Totals.ProjectErrors
		}
	})

	m.mu.Lock()
	m.lastStatus = j.snapshot()
	if err != nil {
		log.Printf("[ERROR] 后台统计任务失败: %v", err)
	} else {
		log.Printf("[INFO] 后台统计任务完成，耗时 %v", finished.Sub(started))
	}
	if m.current == j {
		m.current = nil
	}
	m.mu.Unlock()

	close(j.done)
}

// Wait 阻塞直到当前任务结束，供优雅停机使用。
func (m *JobManager) Wait(timeout time.Duration) bool {
	m.mu.Lock()
	j := m.current
	m.mu.Unlock()
	if j == nil {
		return true
	}

	select {
	case <-j.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Status 返回当前（或最近一次）任务状态。
func (m *JobManager) Status() JobStatus {
	m.mu.Lock()
	j := m.current
	last := m.lastStatus
	m.mu.Unlock()

	if j != nil {
		return j.snapshot()
	}
	return last
}

// Running 表示是否有任务正在执行。
func (m *JobManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current != nil
}

// EmptyRetryAllowed 用于空快照重试节流：达到重试上限、已有任务在跑、
// 或距上次尝试过近时返回 false，避免 GitLab 长时间不可用时反复打满请求。
func (m *JobManager) EmptyRetryAllowed(maxRetries int, minInterval time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		return false
	}
	if m.emptyRetries >= maxRetries {
		return false
	}
	if !m.lastAttempt.IsZero() && time.Since(m.lastAttempt) < minInterval {
		return false
	}
	return true
}

func (m *JobManager) RecordEmptyResult() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emptyRetries++
}

func (m *JobManager) ResetEmptyRetries() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emptyRetries = 0
}

func (m *JobManager) EmptyRetries() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emptyRetries
}

func (j *job) set(st JobStatus) {
	j.mu.Lock()
	j.status = st
	j.mu.Unlock()
}

func (j *job) update(fn func(st *JobStatus)) {
	j.mu.Lock()
	fn(&j.status)
	j.mu.Unlock()
}

func (j *job) snapshot() JobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	st := j.status
	st.Percent = computePercent(st)
	return st
}

func computePercent(st JobStatus) float64 {
	switch st.State {
	case JobSucceeded:
		return 100
	case JobFailed:
		if st.Total > 0 {
			return float64(st.Current) / float64(st.Total) * 100
		}
		return 0
	case JobRunning:
		// 采集阶段占 95%，剩余 5% 留给落盘，避免进度条早早停在 100%。
		if st.Total <= 0 {
			return 1
		}
		p := float64(st.Current) / float64(st.Total) * 95
		if st.Phase == PhasePersist {
			return 97
		}
		if p < 1 {
			return 1
		}
		if p > 95 {
			return 95
		}
		return p
	default:
		return 0
	}
}
