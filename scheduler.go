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
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultStatsCron 默认每日 02:00 执行统计。
	DefaultStatsCron = "0 2 * * *"

	// DefaultStatsTimeout 单次统计任务的整体超时上限。
	DefaultStatsTimeout = 30 * time.Minute
)

// Cron 是极简 5 段式 cron 表达式（分 时 日 月 周）。
//
// 只支持本项目实际需要的子集：`*`、具体数值、逗号列表、区间 a-b、步进 */n。
// 刻意不引入第三方依赖——定时统计的需求足够简单，依赖越少部署越省事。
type Cron struct {
	minute, hour, dom, month, dow uint64 // 位图，最高位 1 表示通配
}

const cronWildcardBit = 63

// ParseCron 解析 5 段式 cron 表达式。
func ParseCron(expr string) (*Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, &CronError{Expr: expr, Reason: "需要 5 个字段（分 时 日 月 周）"}
	}

	ranges := [5][2]int{
		{0, 59}, // minute
		{0, 23}, // hour
		{1, 31}, // day of month
		{1, 12}, // month
		{0, 6},  // day of week (0 = Sunday)
	}

	c := &Cron{}
	targets := []*uint64{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, field := range fields {
		bits, wildcard, err := parseCronField(field, ranges[i][0], ranges[i][1])
		if err != nil {
			return nil, &CronError{Expr: expr, Reason: err.Error()}
		}
		if wildcard {
			bits |= 1 << cronWildcardBit
		}
		*targets[i] = bits
	}
	return c, nil
}

type CronError struct {
	Expr   string
	Reason string
}

func (e *CronError) Error() string {
	return "cron 表达式无效 (" + e.Expr + "): " + e.Reason
}

func parseCronField(field string, min, max int) (uint64, bool, error) {
	wildcard := false
	var bits uint64

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, &fieldError{field, "存在空字段"}
		}

		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			stepStr := part[idx+1:]
			part = part[:idx]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, false, &fieldError{field, "步进值必须是正整数"}
			}
			step = n
		}

		lo, hi := min, max
		switch {
		case part == "*":
			wildcard = wildcard || (step == 1)
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			b, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				return 0, false, &fieldError{field, "区间格式应为 a-b"}
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return 0, false, &fieldError{field, "无法解析数值 " + part}
			}
			lo, hi = v, v
		}

		if lo < min || hi > max || lo > hi {
			return 0, false, &fieldError{field, "取值超出范围 [" + strconv.Itoa(min) + "," + strconv.Itoa(max) + "]"}
		}

		for v := lo; v <= hi; v += step {
			if v <= 63 {
				bits |= 1 << uint(v)
			}
		}
	}

	if bits == 0 {
		return 0, false, &fieldError{field, "未匹配到任何时间点"}
	}
	return bits, wildcard, nil
}

type fieldError struct {
	field  string
	reason string
}

func (e *fieldError) Error() string {
	return "字段 " + e.field + " " + e.reason
}

// Next 返回 after 之后最近一次满足表达式的时间，最多向后搜索 4 年。
func (c *Cron) Next(after time.Time) (time.Time, bool) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)

	for t.Before(limit) {
		if !c.match(t) {
			// 逐级跳跃：月不匹配直接跳到下月，避免逐分钟空转。
			switch {
			case !c.matchMonth(t):
				t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).AddDate(0, 1, 0)
			case !c.matchDay(t):
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
			case !c.matchHour(t):
				t = t.Truncate(time.Hour).Add(time.Hour)
			default:
				t = t.Add(time.Minute)
			}
			continue
		}
		return t, true
	}

	return time.Time{}, false
}

func (c *Cron) match(t time.Time) bool {
	return c.matchMonth(t) && c.matchDay(t) && c.matchHour(t) && c.bitSet(c.minute, t.Minute())
}

func (c *Cron) matchMonth(t time.Time) bool {
	return c.bitSet(c.month, int(t.Month()))
}

func (c *Cron) matchHour(t time.Time) bool {
	return c.bitSet(c.hour, t.Hour())
}

// matchDay 处理标准的 cron 语义：日与周同时限定时取「或」关系，
// 但只要其中一个是通配符，就退化为「与」关系。
func (c *Cron) matchDay(t time.Time) bool {
	domMatch := c.bitSet(c.dom, t.Day())
	dowMatch := c.bitSet(c.dow, int(t.Weekday()))

	domWild := c.isWildcard(c.dom)
	dowWild := c.isWildcard(c.dow)

	switch {
	case domWild && dowWild:
		return true
	case domWild:
		return dowMatch
	case dowWild:
		return domMatch
	default:
		return domMatch || dowMatch
	}
}

func (c *Cron) bitSet(bits uint64, value int) bool {
	if c.isWildcard(bits) {
		return true
	}
	if value < 0 || value > 63 {
		return false
	}
	return bits&(1<<uint(value)) != 0
}

func (c *Cron) isWildcard(bits uint64) bool {
	return bits&(1<<cronWildcardBit) != 0
}

// Describe 生成人类可读的调度描述，用于启动日志和前端展示。
func (c *Cron) Describe() string {
	if c.isWildcard(c.minute) && c.isWildcard(c.hour) &&
		c.isWildcard(c.dom) && c.isWildcard(c.month) && c.isWildcard(c.dow) {
		return "每分钟"
	}
	return "按 cron 表达式调度"
}

// Scheduler 按 cron 表达式周期触发统计任务。
type Scheduler struct {
	cron *Cron
	loc  *time.Location
}

func NewScheduler(expr string) (*Scheduler, error) {
	c, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	return &Scheduler{cron: c, loc: time.Local}, nil
}

func (s *Scheduler) Expression() string { return s.cron.String() }

// Run 阻塞运行调度循环，直到 stop 收到信号。
func (s *Scheduler) Run(stop <-chan struct{}, fire func()) {
	for {
		now := time.Now().In(s.loc)
		next, ok := s.cron.Next(now)
		if !ok {
			log.Printf("[WARN] 无法计算下次统计时间，调度器停止")
			return
		}

		wait := time.Until(next)
		log.Printf("[INFO] 下次统计时间: %s (还有 %s)", next.Format("2006-01-02 15:04:05"), wait.Truncate(time.Second))

		timer := time.NewTimer(wait)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			fire()
		}
	}
}

// String 还原表达式，便于日志核对。
func (c *Cron) String() string {
	return strings.Join([]string{
		renderField(c.minute),
		renderField(c.hour),
		renderField(c.dom),
		renderField(c.month),
		renderField(c.dow),
	}, " ")
}

func renderField(bits uint64) string {
	if bits&(1<<cronWildcardBit) != 0 {
		return "*"
	}
	var values []string
	for v := 0; v <= 63; v++ {
		if bits&(1<<uint(v)) != 0 {
			values = append(values, strconv.Itoa(v))
		}
	}
	return strings.Join(values, ",")
}
