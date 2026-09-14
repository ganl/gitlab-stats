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
	"sort"
	"strconv"
	"strings"
	"time"
)

const topContributorLimit = 10

func formatKey(t time.Time, period string) string {
	switch period {
	case "week":
		year, week := t.ISOWeek()
		return strconv.Itoa(year) + "-W" + strconv.Itoa(week)
	case "month":
		return t.Format("2006-01")
	default:
		return t.Format("2006-01-02")
	}
}

// dateLayout 是快照中所有日期字段的序列化格式。
// 采用「年-月-日」定长字符串，使日期比较可直接用字典序完成。
const dateLayout = "2006-01-02"

// statsLocation 是统计口径所用的固定时区（东八区）。
//
// 不能用提交自带的时区。GitLab 完整保留作者提交时的时区偏移，同一个 UTC 时刻
// 由美西（-07:00）和中国（+08:00）的提交者写出，会得到相差一天的两个日期：
// 前者被归到前一天，于是这条记录落到了采集区间之外——增量合并以「日期」为
// 替换单位，区间外的日期不在替换范围内，就会与上一份快照的同日记录并存，
// 产生重复。实测某实例上美西时区的提交因此被归错日期。
//
// 用 FixedZone 而非 LoadLocation("Asia/Shanghai")：后者依赖 tzdata，
// 在缺少时区数据库的环境（如精简的 Windows 镜像）会直接失败。
var statsLocation = time.FixedZone("UTC+8", 8*60*60)

// dateKey 与 formatKey(t, "day") 等价，但统一按 statsLocation 归日，
// 保证同一 UTC 时刻在任何提交者时区下都落到同一天。
func dateKey(t time.Time) string {
	return t.In(statsLocation).Format(dateLayout)
}

// startOfDay 返回 t 所在自然日（statsLocation 视角）的 00:00:00。
func startOfDay(t time.Time) time.Time {
	y, m, d := t.In(statsLocation).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, statsLocation)
}

// endOfDay 返回 t 所在自然日（statsLocation 视角）的 23:59:59。
func endOfDay(t time.Time) time.Time {
	y, m, d := t.In(statsLocation).Date()
	return time.Date(y, m, d, 23, 59, 59, 0, statsLocation)
}

// cutoffDate 返回展示窗口的起始日期（含当天），口径为「最近 days 个自然日」。
//
// 起点取 days-1 而非 days：days=7 表示含今天在内的 7 天，应从 6 天前算起。
// 旧实现直接用 -days，使「最近 7 天」实际覆盖 8 个自然日——下拉框写着 7 天却给 8 天。
// 实测 7 天窗口多算了第 8 天（2026-09-07）的 80 次提交，用独立脚本按自然日复算才发现。
//
// 采集侧（collector.go 的 planRanges / MergeIncremental）仍按 WindowDays 取 366 天，
// 比任何展示窗口都宽一天，因此收紧展示窗口不会造成数据缺口。
func cutoffDate(days int) string {
	if days < 1 {
		days = 1
	}
	return dateKey(time.Now().AddDate(0, 0, -(days - 1)))
}

// withinWindow 判断日期字符串是否落在最近 days 个自然日内（含今天）。
func withinWindow(date string, days int) bool {
	return date >= cutoffDate(days)
}

// authorGroupKey 产出贡献者归并键与展示名。
//
// 优先级：GitLab 用户 ID → 邮箱 → 用户名 → 姓名（兜底）。
// 前两者跨改名、跨邮箱变更保持稳定；姓名不具备规律，只在无任何稳定标识时兜底。
// 这样既不会把同一个人按改名拆开，也不会把同名账号合并。
func authorGroupKey(d *DailyStat) (key, name string) {
	name = d.Name
	if name == "" {
		name = "Unknown"
	}
	switch {
	case d.AuthorKey > 0:
		return "id:" + strconv.Itoa(d.AuthorKey), name
	case d.Email != "":
		return "email:" + normalizeString(d.Email), name
	case d.Username != "":
		return "un:" + normalizeString(d.Username), name
	default:
		return "name:" + normalizeString(name), name
	}
}

// CommitFrequency 是按 period 聚合后的提交趋势，公共接口保持兼容。
func BuildCommitFrequency(snap *Snapshot, period string, days int) []CommitFrequency {
	buckets := make(map[string]int)
	order := make([]string, 0)

	for i := range snap.Daily {
		d := &snap.Daily[i]
		if d.Commits == 0 || !withinWindow(d.Date, days) {
			continue
		}
		if snap.IsAuthorExcluded(d) {
			continue
		}
		t, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			continue
		}
		key := formatKey(t, period)
		if _, seen := buckets[key]; !seen {
			order = append(order, key)
		}
		buckets[key] += d.Commits
	}

	result := make([]CommitFrequency, 0, len(buckets))
	for _, k := range order {
		result = append(result, CommitFrequency{Date: k, Count: buckets[k]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result
}

// BuildMRStatistics 从快照重建 MR 统计，输出结构与旧接口完全一致。
func BuildMRStatistics(snap *Snapshot, period string, days int, gitlabURL string) MRStatistics {
	stats := MRStatistics{
		Authors:     []MRAuthor{},
		MergedByDay: []MergedByDay{},
	}

	type authorAgg struct {
		name       string
		username   string
		profileURL string
		count      int
	}
	authorCounts := make(map[string]*authorAgg)
	mergedByPeriod := make(map[string]int)

	for i := range snap.Daily {
		d := &snap.Daily[i]
		if !withinWindow(d.Date, days) {
			continue
		}
		if d.CreatedMRs == 0 && d.MergedMRs == 0 && d.OpenedMRs == 0 && d.ClosedMRs == 0 {
			continue
		}
		if snap.IsAuthorExcluded(d) {
			continue
		}

		// 按 authorKey 归并而非姓名：GitLab 用户 ID 稳定，而姓名会随改名变化、
		// 也可能出现同名不同账号。缺失稳定 ID 时才退回姓名分组。
		key, displayName := authorGroupKey(d)
		agg, ok := authorCounts[key]
		if !ok {
			agg = &authorAgg{name: displayName, username: d.Username, profileURL: d.ProfileURL}
			authorCounts[key] = agg
		}
		// 优先用带 GitLab 身份的那条记录补齐展示信息。
		if agg.profileURL == "" && d.ProfileURL != "" {
			agg.username = d.Username
			agg.profileURL = d.ProfileURL
		}
		if agg.name == "" && displayName != "" {
			agg.name = displayName
		}
		agg.count += d.CreatedMRs

		stats.Merged += d.MergedMRs
		stats.Opened += d.OpenedMRs
		stats.Closed += d.ClosedMRs

		if d.MergedMRs > 0 {
			t, err := time.Parse("2006-01-02", d.Date)
			if err == nil {
				mergedByPeriod[formatKey(t, period)] += d.MergedMRs
			}
		}
	}

	stats.Total = stats.Merged + stats.Opened + stats.Closed

	authors := make([]MRAuthor, 0, len(authorCounts))
	for _, agg := range authorCounts {
		profileURL := agg.profileURL
		if profileURL == "" && agg.username != "" {
			profileURL = strings.TrimSuffix(gitlabURL, "/") + "/" + agg.username
		}
		authors = append(authors, MRAuthor{
			Name:       agg.name,
			Username:   agg.username,
			ProfileURL: profileURL,
			Count:      agg.count,
		})
	}
	sort.Slice(authors, func(i, j int) bool {
		if authors[i].Count != authors[j].Count {
			return authors[i].Count > authors[j].Count
		}
		return authors[i].Name < authors[j].Name
	})
	if len(authors) > topContributorLimit {
		authors = authors[:topContributorLimit]
	}
	stats.Authors = authors

	mergedByDay := make([]MergedByDay, 0, len(mergedByPeriod))
	for date, count := range mergedByPeriod {
		mergedByDay = append(mergedByDay, MergedByDay{Date: date, Count: count})
	}
	sort.Slice(mergedByDay, func(i, j int) bool { return mergedByDay[i].Date < mergedByDay[j].Date })
	stats.MergedByDay = mergedByDay

	return stats
}

// BuildCodeVolume 从快照重建代码量与未参与成员检测。
//
// 代码量只统计提交；但「是否参与」按提交与 MR 任一存在判定——
// 本实例大量开发者走 MR 流程（代码在 bugfix/*、feature/* 分支上提交，
// 由 MR 合入目标分支），其提交可能完全不出现在默认分支的提交列表里。
// 若只按提交判定，这些人会被误列为「零提交成员」：
// 实测该名单中约有一半属于此类，其中有人一年创建了数百个 MR。
func BuildCodeVolume(snap *Snapshot, days int, gitlabURL string) CodeVolume {
	stats := CodeVolume{
		TopContributors: []TopContributor{},
		InactiveMembers: []InactiveMember{},
	}

	type contributorAgg struct {
		name       string
		username   string
		profileURL string
		additions  int
		deletions  int
		commits    int
	}
	contributors := make(map[string]*contributorAgg)
	activeByID := make(map[int]bool)
	activeByUsername := make(map[string]bool)
	activeByEmail := make(map[string]bool)

	for i := range snap.Daily {
		d := &snap.Daily[i]
		if !withinWindow(d.Date, days) {
			continue
		}
		// 被排除的自动化身份不计入代码量，也不因其活动而判定为参与。
		if snap.IsAuthorExcluded(d) {
			continue
		}

		// 活跃判定：窗口内有过提交或参与过 MR，任一即视为参与。
		if d.HasActivity() {
			if d.AuthorKey > 0 {
				activeByID[d.AuthorKey] = true
			}
			if d.Username != "" {
				activeByUsername[normalizeString(d.Username)] = true
			}
			if d.Email != "" {
				activeByEmail[normalizeString(d.Email)] = true
			}
		}

		// 代码量维度只认提交，MR 本身不产生增删行数。
		if d.Commits == 0 {
			continue
		}

		stats.TotalCommits += d.Commits
		stats.TotalAdditions += d.Additions
		stats.TotalDeletions += d.Deletions

		name := d.Name
		if name == "" {
			name = "Unknown"
		}
		// 与 MR 统计一致：按稳定身份归并，避免改名/同名导致的错拆错并。
		key, _ := authorGroupKey(d)
		agg, ok := contributors[key]
		if !ok {
			agg = &contributorAgg{name: name, username: d.Username, profileURL: d.ProfileURL}
			contributors[key] = agg
		}
		if agg.name == "" && name != "" {
			agg.name = name
		}
		if agg.username == "" && d.Username != "" {
			agg.username = d.Username
		}
		if agg.profileURL == "" && d.ProfileURL != "" {
			agg.profileURL = d.ProfileURL
		}
		agg.additions += d.Additions
		agg.deletions += d.Deletions
		agg.commits += d.Commits
	}

	list := make([]TopContributor, 0, len(contributors))
	for _, agg := range contributors {
		profileURL := agg.profileURL
		if profileURL == "" && agg.username != "" {
			profileURL = strings.TrimSuffix(gitlabURL, "/") + "/" + agg.username
		}
		list = append(list, TopContributor{
			Name:       agg.name,
			Username:   agg.username,
			ProfileURL: profileURL,
			Additions:  agg.additions,
			Deletions:  agg.deletions,
			Commits:    agg.commits,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Commits != list[j].Commits {
			return list[i].Commits > list[j].Commits
		}
		return list[i].Name < list[j].Name
	})
	if len(list) > topContributorLimit {
		list = list[:topContributorLimit]
	}
	stats.TopContributors = list

	// 未参与成员检测：遍历用户表并逐条排除不该被点名的身份。
	//
	// 四类账号不进这个名单，理由各不相同：
	//
	//  1. bot=true 的机器人 / 服务账号——不参与考核，否则报表会把
	//     自动化身份当成「未参与开发的成员」。
	//  2. ExcludeAuthors 中的账号——GitLab 未标记 bot 的自动化账号
	//     （打包脚本、发布机器人）在日报里被跳过，若用户表不一起跳过，
	//     它们反而会因为「没有任何活跃标记」而落进未参与名单，口径自相矛盾。
	//  3. ExemptFromInactive 中的账号——管理层 / 领导等本就不承担编码指标的
	//     成员。该名单只影响本名单，其提交与 MR 仍照常计入统计。
	//  4. 已禁用（离职）账号——用户表只采集 GitLab 侧 active 用户
	//     （GetAllUsers 带 active=true），离职账号不在表中，自然进不了名单。
	//     这一点是刻意保留的：离职同事的历史提交仍会被采集、并计入贡献统计，
	//     但不该被当作「在职却未参与开发」点名。修改用户表采集口径时须留意。
	//
	// 三条活跃标记按「用户 ID → 用户名 → 邮箱」依次判定，任一命中即为参与——
	// 用户表的记录与日报记录可能来自不同的识别路径（如 MR 只有用户名、
	// 提交只有邮箱），单看一条会漏判。
	for i := range snap.Totals.Users {
		u := &snap.Totals.Users[i]
		if u.Bot {
			continue
		}
		if snap.IsUserExcluded(u) {
			continue
		}
		if snap.IsUserExemptFromInactive(u) {
			continue
		}
		if u.ID > 0 && activeByID[u.ID] {
			continue
		}
		if u.Username != "" && activeByUsername[normalizeString(u.Username)] {
			continue
		}
		if u.Email != "" && activeByEmail[normalizeString(u.Email)] {
			continue
		}
		profileURL := u.ProfileURL
		if profileURL == "" && u.Username != "" {
			profileURL = strings.TrimSuffix(gitlabURL, "/") + "/" + u.Username
		}
		stats.InactiveMembers = append(stats.InactiveMembers, InactiveMember{
			Name:       u.Name,
			Username:   u.Username,
			ProfileURL: profileURL,
		})
	}
	sort.Slice(stats.InactiveMembers, func(i, j int) bool {
		return stats.InactiveMembers[i].Name < stats.InactiveMembers[j].Name
	})

	return stats
}
