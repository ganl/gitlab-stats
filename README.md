# GitLab Stats

A high-performance GitLab code statistics and analysis tool that supports commit frequency, MR status, and code volume contribution analysis.

[中文版文档](README-CN.md)

## Features

- 📈 **Commit Frequency Statistics** - Analyze code commit trends by day/week/month
- 🔄 **MR Status Analysis** - Merge request status statistics and trends
- 👥 **Contributor Leaderboard** - Rank contributors by commit count and code volume
- 🔍 **Inactive Member Detection** - Identify members with no activity at all in the window
  (neither commits nor Merge Requests; bot and excluded accounts are skipped)
- 🌿 **All-Branch Collection** - Commits are collected across every branch (`all=true`), not just
  the default branch, so MR-based workflows are counted correctly
- 🗓️ **Incremental Backfill** - The first run collects only a recent chunk; each later run
  advances one chunk further back until the full window is covered, keeping every run bounded
- 🗓️ **Scheduled Offline Statistics** - Statistics run on a cron schedule in the background; the frontend always reads the latest cached snapshot
- 📊 **Progress Reporting** - Live collection progress (phase, processed/total projects, percentage) while a run is in progress
- 🕒 **Data Freshness Indicator** - Shows snapshot time, age, collection duration, and the next scheduled run
- ⚡ **Concurrent Processing** - High-performance concurrent requests for faster data retrieval
- 🔒 **Read-Only** - Only issues GET requests; never modifies the GitLab instance
- 📊 **Visual Interface** - Beautiful web dashboard

## Quick Start

### Installation

```bash
git clone https://github.com/ganl/gitlab-stats.git
cd gitlab-stats
go build -o gitlab-stats
```

### Configuration

Copy and modify the configuration file:

```bash
# Linux/macOS
cp config.json.dist config.json

# Windows
copy config.json.dist config.json
```

Edit `config.json`:

```json
{
  "gitlab_url": "https://gitlab.example.com",
  "token": "your-gitlab-access-token",
  "port": 8080,
  "max_concurrent": 20,
  "request_timeout": "30s",
  "cache_enabled": true,
  "cache_ttl": "5m",
  "log_enabled": false,
  "log_requests": true,
  "log_responses": false,
  "data_dir": "data",
  "stats_enabled": true,
  "stats_cron": "0 2 * * *",
  "stats_on_start": true,
  "stats_timeout": "30m",
  "scan_all_branches": true,
  "refresh_overlap_days": 30,
  "backfill_chunk_days": 30,
  "exclude_authors": [],
  "exempt_from_inactive": []
}
```

**Configuration Options:**

| Option | Description |
|--------|-------------|
| `gitlab_url` | GitLab instance URL (required) |
| `token` | GitLab access token (required) |
| `port` | Web service port (default: 8080) |
| `max_concurrent` | Maximum concurrent requests (1-100, default: 20) |
| `request_timeout` | Single request timeout (default: 30s) |
| `cache_enabled` | Enable raw API response caching (default: true) |
| `cache_ttl` | Raw response cache TTL (default: 5m) |
| `log_enabled` | Enable logging (default: false) |
| `log_requests` | Log request details (default: true) |
| `log_responses` | Log response details (default: false) |
| `data_dir` | Directory for statistics snapshots (default: `data`) |
| `stats_enabled` | Enable scheduled statistics (default: true) |
| `stats_cron` | 5-field cron expression, local time (default: `0 2 * * *`, i.e. daily at 02:00) |
| `stats_on_start` | Run a collection right after startup (default: true) |
| `stats_timeout` | Overall timeout for one collection run (default: 30m) |
| `scan_all_branches` | Collect commits from **all** branches (`all=true`) instead of only the default branch (default: true) |
| `refresh_overlap_days` | How far back each incremental run re-collects, to pick up amended/rebased commits and late state changes (default: 30) |
| `backfill_chunk_days` | Span of one historical backfill chunk; the first run collects one chunk and each later run advances one more (default: 30) |
| `exclude_authors` | Author identifiers excluded from contribution stats (username / email / name, case-insensitive) |
| `exempt_from_inactive` | Accounts exempt from the inactive-member report only; their commits and MRs still count everywhere else (username / email / name, case-insensitive) |

`cache_ttl` and `stats_cron` are intentionally independent: the former caches raw GitLab API
responses during a collection run, while the latter controls how often the snapshot is rebuilt.

#### About `scan_all_branches`

Collecting only the default branch badly undercounts real work on this kind of instance: much
development happens on `bugfix/*` and `feature/*` branches and reaches the default branch only
through a Merge Request. Measured on a real instance over a 30-day window:

| Project | Default branch | All branches |
| --- | --- | --- |
| `project-a` | 0 | 14 |
| `project-b` | 1 | 680 |
| `project-c` | 0 | 953 |

With `all=true`, GitLab de-duplicates commits by id across branches (measured duplicate rate:
0.0%), so no client-side de-duplication is needed. The cost is API pages, not correctness:
`with_stats=true` itself adds only ~1.1x, while switching to all branches multiplies the row
count by roughly 6.8x and the wall-clock time by roughly 7.2x.

#### About incremental collection

Because a single large project can take several minutes to enumerate across all branches
(measured: ~170 pages / ~16.6k commits / ~580s for one project over 365 days), a full one-shot
collection is not practical. Collection is therefore **incremental**:

- `refresh_overlap_days` (default 30) — every run re-collects this recent span, so amended,
  rebased, cherry-picked commits and late MR state changes are picked up.
- `backfill_chunk_days` (default 30) — the first run collects only the most recent chunk; each
  later run advances one chunk further back until the whole `WindowDays` window is covered.

The snapshot tracks its own progress in `covered_from`: while it is later than the window start,
the dashboard shows "历史回填中" (backfilling) together with the covered date; once it reaches the
window start the coverage is reported as complete. Runs are bounded regardless of window size —
measured ~46s per run against a ~450-project instance with a 30-day chunk.

Merging is done **per date**: a day this run re-collected is replaced wholesale, every other day
is kept. Counts for a day are never added twice, so growing the window cannot inflate totals.

A single run produces **two non-adjacent ranges** — the recent refresh plus one older backfill
chunk — with already-collected history in between. The replacement scope must therefore be the
*set* of ranges rather than their span: taking the earliest range start as a single boundary
drops the history in between, which this run never re-collected. Measured on a real instance, a
full 30-day stretch silently fell to 4 records out of ~1700 while `covered_from` kept advancing
and reported it as covered.

Because the replacement unit is a whole day, every range boundary is aligned to a natural-day
edge. A range starting mid-day would collect only part of that day while `covered_from` reported
it as covered, so the previous snapshot's full day would be dropped and the date truncated —
measured on a real instance, one date ended up with 3 records against 65 and 9 on its neighbours.

#### About date bucketing

Dates are bucketed in a **fixed timezone (UTC+8)**, not in each author's own timezone. GitLab
preserves the author's original UTC offset, so the same instant written by a contributor in
US Pacific (`-07:00`) and one in China (`+08:00`) would otherwise land on two different days:
one person's work gets split across dates, and close to a range boundary the record can fall
outside the range entirely — where the per-date merge cannot replace it, leaving two rows for
the same person and day. Measured on a real instance, a commit stamped
`2026-08-14T22:07:09-07:00` (= `2026-08-15T05:07:09Z`, squarely inside the refresh range) was
bucketed one day early.

#### About `exclude_authors`

GitLab's `bot` flag (group bot / project bot / service token) only covers automation accounts
the platform itself recognizes. Another class of automation account is *not* flagged — most
notably **packaging / release accounts**, which touch only a version string on every build.
Their commits are extremely frequent and their `additions` and `deletions` stay nearly equal,
while their real code contribution is close to zero. They crowd the contribution ranking but
cannot be detected through the API, so declaring them explicitly is the only reliable option:

```json
{
  "exclude_authors": ["packaging-bot", "release-bot"]
}
```

**How to spot such accounts:** look for entries in the ranking with a very high commit count but
very few changed lines, and check whether `additions` stays equal to `deletions` over time. In one
real instance:

| Account | Commits | Lines changed | Symmetry rate |
| --- | --- | --- | --- |
| `release-bot` | ~1,200 | ~1,500 | 99.6% |
| `packaging-bot` | ~520 | ~1,000 | 100% |

A human's commits essentially never keep additions and deletions balanced over the long run.
Once identified, add the account's username (or email, or name) to `exclude_authors`.

A second class shows up the same way and is just as common: **build / CI container identities**.
When a pipeline commits without configuring `git user.email`, the identity falls back to
`root@<container-host>`, which matches no GitLab account — so it lands in the snapshot with
`author_key: 0` and appears in the ranking under the name `root`:

| Identity | Commits | Lines changed | Symmetry rate |
| --- | --- | --- | --- |
| `root@ci-runner-01` | ~3,000 | ~10,200 | 100% |
| `root@ci-runner-02` | ~270 | ~1.0M | 0.4% |

Both extremes are machine signatures: exact symmetry means every build rewrites one line pair,
while 0.4% means a single bulk import of ~1M lines against a few thousand deletions. No human
produces either pattern. Note that the displayed name is just `root` — list the **full address**
(`root@<host>`), never the bare `root`: every container shares that name, and a name match would
also catch the GitLab administrator account.

Excluded authors are skipped in commit frequency, code volume, contributor ranking and MR stats.
The same list is also applied to the user table, so an excluded account cannot surface in the
inactive-member report merely because it has no activity of its own.
The raw records are still kept in the snapshot, and the effective list is recorded in the
snapshot's `excluded_authors` field, so a change of scope stays traceable and recomputable.

#### About `exempt_from_inactive`

`exclude_authors` removes an account from the statistics altogether. That is right for automation
and wrong for people. Some members hold a position where day-to-day coding is not part of the job
— **management, team leads, and QA / product roles** most typically. They may still commit
occasionally, and those commits are real work that belongs in the numbers; what does not belong
is their name on the **inactive-member** report.

That report is window-sensitive: it is computed against the selected display range, so someone
with a handful of commits spread over the year drops into it as soon as the range narrows to
"last 30 days" or "last 7 days". Use `exempt_from_inactive` to take such accounts off that report
while leaving every other metric untouched:

```json
{
  "exclude_authors": ["packaging-bot", "release-bot"],
  "exempt_from_inactive": ["team-lead"]
}
```

The two lists differ in scope, which is the whole point of having both:

| List | Commit frequency | Code volume | Ranking | MR stats | Inactive report |
| --- | --- | --- | --- | --- | --- |
| `exclude_authors` | excluded | excluded | excluded | excluded | hidden |
| `exempt_from_inactive` | counted | counted | counted | counted | hidden |

Both accept a username, email address, or display name, matched case-insensitively and
whitespace-insensitively. The effective list is stored in the snapshot's `exempt_from_inactive`
field, so the composition of the inactive report stays traceable and recomputable.

#### About the user table

Members are read from GitLab with `active=true`, so the collected user table contains only
currently enabled accounts. The filter is not cosmetic: on a real instance it narrowed the
account list down to a minority of all accounts, the remainder being blocked, deactivated or
banned.

It also drops GitLab's own system accounts
(`GitLabDuo`, `GitLab-Admin-Bot`, `support-bot`, `alert-bot`) and the `Ghost User` placeholder.
Nothing of value is lost — the four bots are automation identities, and the Ghost User is where
GitLab files commits of deleted accounts.

This filter is deliberate, and it is what keeps **departed colleagues out of the inactive-member
report**. Their historical commits are still collected from the projects themselves and still
count towards commit frequency, code volume and the ranking — an ex-employee's work inside the
window did happen — but they are not named as members who are "employed yet inactive".

Loading the full account list instead would put every blocked / deactivated account into the user
table and, since none of them has recent activity, straight into the inactive report. If you ever
change that filter, re-check the inactive-member loop in `aggregate.go`: the two are coupled by
design.

### Getting a Token

1. Log in to GitLab
2. Go to Profile → Access Tokens
3. Generate a new token with the following scopes:
   - `read_api`
   - `read_user`

### Running

```bash
# Linux/macOS
./gitlab-stats

# Windows
.\gitlab-stats.exe
```

Then visit http://localhost:8080

On first start the service collects statistics immediately (unless `stats_on_start` is `false`).
While collection runs, the dashboard shows a live progress bar. Once finished, the snapshot is
written to `data/stats.json` and every subsequent page load is served from that file — no GitLab
round-trip on read.

## How It Works

The design separates **collection** from **serving**:

```
       cron trigger / manual trigger
                 │
                 ▼
        ┌─────────────────┐
        │    Collector    │  concurrently reads commits + MRs (GET only)
        └────────┬────────┘
                 │  aggregates into per-author × per-day rows
                 ▼
        ┌─────────────────┐
        │ data/stats.json │  atomic write (temp file + rename)
        └────────┬────────┘
                 │
                 ▼
        ┌─────────────────┐
        │   HTTP handlers │  read snapshot from memory, aggregate on the fly
        └─────────────────┘
```

Key properties:

- **One collection serves every panel.** All metrics are derived from the same per-author ×
  per-day rows, so commit frequency, code volume, contributor ranking, inactive-member detection,
  and MR trends never require a second pass.
- **The display range is decorative.** `period` / `days` query parameters narrow the in-memory
  aggregation only; changing them triggers no GitLab request. The snapshot window is fixed at
  365 days (`WindowDays`), which collection fills incrementally.
- **Activity means commits *or* MRs.** A member counts as participating when the window contains
  either commits or any Merge Request activity. Judging by commits alone misclassifies
  MR-driven contributors: on a real instance roughly half of the members reported as
  "zero-commit" were in fact active, one of them having created several hundred MRs in a year.
- **Identity is matched on stable dimensions.** GitLab user id, then email, then username, with
  the display name as a last resort only. Display names are not unique and change at will —
  matching on them both merged distinct people (one name mapped to 3 different emails) and split
  the same person across identities.
- **Snapshot writes are atomic.** Data is written to a temp file and renamed into place, so a
  crash mid-write cannot leave a half-parsed `stats.json` behind.
- **Failures are isolated.** A failing project is recorded in `project_errors` and surfaced in
  the dashboard; the rest of the collection proceeds.
- **Missing commit stats are flagged.** GitLab omits `stats` for some commits (merge commits,
  oversized commits). Such commits are counted but the affected row is marked
  `stats_incomplete` rather than silently contributing 0 changed lines.
- **Bot, excluded and exempt accounts are handled apart.** Accounts flagged `bot: true` by GitLab
  (group bots, project bots, service tokens) and accounts listed in `exclude_authors` are excluded
  from both the contribution stats and the inactive-member report. Accounts in
  `exempt_from_inactive` are hidden from the inactive-member report only — their commits and MRs
  keep counting under every other metric. Accounts disabled on the GitLab side are not collected at
  all, so a departed colleague is never reported as inactive.
- **Restart-safe.** On startup the snapshot is loaded from disk, so the dashboard is usable
  immediately and a restart does not force a re-collection.
- **No stale front-end.** The page is served with `no-store`, so a rebuilt binary always
  reaches the browser. Embedded assets cannot supply `Last-Modified` (embed has no
  modification time), so they carry a content-hash `ETag` instead and revalidate on each
  request — a 304 when unchanged, a fresh copy the moment the bytes differ.

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Web dashboard |
| `/health` | GET | Health check (includes snapshot time and job state) |
| `/api/stats/status` | GET | Snapshot metadata + collection job progress |
| `/api/stats/refresh` | POST | Trigger a collection run (409 if one is already running) |
| `/api/stats/commit-frequency` | GET | Commit frequency statistics |
| `/api/stats/mr-statistics` | GET | MR status statistics |
| `/api/stats/code-volume` | GET | Code volume statistics |

**Query Parameters:**

- `period` - Statistics period: `day` (default) / `week` / `month`
- `days` - Display range in days: default 90, any positive value up to the snapshot
  window (365). The range is **N natural days including today** — `days=7` covers today
  plus the six days before it. There is no lower bound, so short ranges such as 7 days
  are valid.

### Snapshot Status Response

`GET /api/stats/status` is the single source the dashboard uses to decide between rendering
cached data and showing a progress bar:

```json
{
  "job": {
    "state": "running",
    "phase": "collecting",
    "message": "正在从 GitLab 采集数据...",
    "current": 440,
    "total": 450,
    "percent": 94.2,
    "trigger": "schedule"
  },
  "snapshot": {
    "generated_at": "2026-09-13T16:04:00+08:00",
    "duration_ms": 39000,
    "records": 9000,
    "projects_scanned": 450,
    "users_scanned": 150,
    "failed_projects": 1,
    "age_seconds": 60,
    "stale": false,
    "covered_from": "2026-07-16",
    "scan_all_branches": true,
    "window_days": 365
  },
  "schedule": {
    "enabled": true,
    "cron": "0 2 * * *",
    "next_run": "2026-09-14T02:00:00+08:00",
    "window_days": 365
  }
}
```

`covered_from` is the earliest date the snapshot actually covers. While it is later than the
window start the dashboard reports how far back the history reaches; once it equals the window
start, coverage is complete. See [About incremental collection](#about-incremental-collection).

While no snapshot exists yet, the statistics endpoints return **503** (or **202** if a
collection is currently running) with a structured error, so the dashboard can distinguish
"no data yet" from "loading".

## Development

### Project Structure

```
gitlab-stats/
├── main.go          # Entry point: wiring, server lifecycle, graceful shutdown
├── config.go        # Configuration loading and validation
├── types.go         # Data structures (includes tolerant Commit JSON handling)
├── gitlab.go        # GitLab API client (read-only)
├── collector.go     # Concurrent collection into a snapshot
├── aggregate.go     # Snapshot → API payload aggregation
├── store.go         # Atomic snapshot persistence
├── job.go           # Background job state machine and progress
├── scheduler.go     # Dependency-free 5-field cron scheduler
├── cache.go         # In-flight raw API response cache
├── handler.go       # HTTP handlers (serve from snapshot only)
├── mockgitlab.go    # Standalone read-only mock server for smoke tests
├── config.json      # Configuration file
├── go.mod           # Go module
├── static/
│   └── vendor/
│       └── chart.umd.min.js   # Embedded frontend chart library (no network needed)
├── templates/
│   └── index.html   # Web dashboard
└── *_test.go        # Test files
```

> `static/vendor/` must be committed: it is compiled into the binary via
> `go:embed static/*` in `main.go`, and a missing file breaks the build.
> `.gitignore` whitelists it with `!static/vendor/**`.

### Frontend Assets

The chart library (Chart.js 4.4.0) is embedded into the executable via `go:embed` and served
from `/static/`. Reasons:

- **Works on isolated networks**: internal deployments often cannot reach a public CDN, which
  would break every chart on the page
- **Single-file deployment**: ship one executable, no extra asset copying
- **No external dependency**: immune to third-party CDN availability

### Running Tests

```bash
# Run all tests
go test -v

# View coverage
go test -cover
```

### Smoke Test Without a Token

`mockgitlab.go` is a standalone read-only GitLab API mock (excluded from the build via
`//go:build ignore`). It simulates 120 projects — more than one page, so pagination is
exercised — and lets you verify the full pipeline without a real GitLab instance:

```bash
# Terminal 1
go run mockgitlab.go          # listens on :9999

# Terminal 2 — point gitlab_url at http://localhost:9999, then
go build -o gitlab-stats . && ./gitlab-stats
```

### Rebuilding

```bash
go build -o gitlab-stats
```

## Performance Notes

- **Concurrency Control** - Control concurrent requests via `max_concurrent` to prevent rate limiting
- **Connection Pooling** - HTTP client configured for connection reuse
- **Single Pass** - One collection feeds every panel; commits are fetched once, not per metric
- **Pagination Handling** - Automatic GitLab API pagination for complete data retrieval
- **Cheap Reads** - Page loads and range changes never touch GitLab
- **Bounded Runs** - Incremental backfill keeps per-run cost independent of the window size

A reference run against an instance with ~450 projects and ~150 active users completes in
roughly 40-46 seconds at `max_concurrent: 20` and writes a ~2.9 MB snapshot.

Collection cost is dominated by **API pages**, not by `with_stats`:

| Configuration | Commits (25-project sample, 365d) | Wall clock | Extrapolated to ~450 projects |
| --- | --- | --- | --- |
| Default branch only | ~4,500 | ~120s | ~110s |
| All branches (`all=true`) | ~30,800 | ~860s | ~780s |

`with_stats=true` was measured at ~1.1x overhead on its own (0.70s vs 0.74s for page 30 of a
project). The single largest projects dominate the tail: one project took ~170 pages / ~16.6k
commits / ~580s on its own. This is why a full one-shot backfill is not viable and collection
advances in chunks instead.

## Troubleshooting

### Configuration Loading Failed

Check if `config.json` exists and has the correct format. An invalid `stats_cron` fails
validation at startup rather than being silently ignored.

### GitLab API Errors

Verify:
- Token has sufficient permissions (`read_api`, `read_user`)
- Token has not expired
- GitLab URL is correct
- Network connection is working

### Dashboard Shows "部分项目采集失败"

Some projects could not be read (commonly `404 Repository Not Found` for empty projects).
Open `/api/stats/status` and inspect `snapshot.failed_projects` behavior via the log output —
failed projects are listed in the collection log and in `data/stats.json` under
`totals.project_errors`.

### Dashboard Shows "数据已超过 26 小时未更新"

The scheduled run has not succeeded recently. Check whether the process is still running and
whether GitLab is reachable, then trigger a manual run with the "重新统计" button.

### Performance Issues

Adjust `max_concurrent`:

```json
{
  "max_concurrent": 50
}
```

## License

This project is licensed under the Apache License, Version 2.0.

See the [LICENSE](LICENSE) file or visit http://www.apache.org/licenses/LICENSE-2.0 for full terms.
