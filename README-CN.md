# GitLab 统计工具

一个高性能的 GitLab 代码统计分析工具，支持提交频率、MR 状态、代码量贡献分析等功能。

[English Documentation](README.md)

## 功能特性

- 📈 **提交频率统计** - 按日/周/月统计代码提交趋势
- 🔄 **MR 状态分析** - 合并请求状态统计和趋势
- 👥 **贡献者排行榜** - 按提交次数和代码量排名
- 🔍 **未参与成员检测** - 找出窗口内既无提交、也未参与任何 Merge Request 的成员
  （自动排除机器人账号与排除名单内的账号）
- 🌿 **全分支采集** - 提交按所有分支采集（`all=true`），而非只取默认分支，
  走 MR 流程的开发不会被漏计
- 🗓️ **增量回填** - 首次只采最近一块，此后每次运行向前推进一块，
  直到覆盖满整个统计窗口，使每次运行的成本有界
- 🗓️ **定时离线统计** - 后台按 cron 计划执行统计，前端始终读取最近一次缓存结果
- 📊 **进度展示** - 统计进行中展示实时进度（阶段、已处理/总项目数、百分比）
- 🕒 **数据新鲜度** - 面板展示快照时间、距今多久、统计耗时与下次更新时间
- ⚡ **并发处理** - 高性能并发请求，数据获取更快
- 🔒 **只读访问** - 仅发送 GET 请求，不会修改 GitLab 实例任何数据
- 📊 **可视化界面** - 美观的 Web 控制面板

## 快速开始

### 安装

```bash
git clone <repository-url>
cd gitlab-stats
go build -o gitlab-stats.exe
```

### 配置

复制配置文件并修改：

```bash
# Windows
copy config.json.dist config.json
```

编辑 `config.json`：

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

**配置说明：**

| 配置项 | 说明 |
|--------|------|
| `gitlab_url` | GitLab 实例地址（必填） |
| `token` | GitLab 访问 Token（必填） |
| `port` | Web 服务端口（默认 8080） |
| `max_concurrent` | 最大并发请求数（1-100，默认 20） |
| `request_timeout` | 单请求超时时间（默认 30s） |
| `cache_enabled` | 是否启用原始响应缓存（默认 true） |
| `cache_ttl` | 原始响应缓存过期时间（默认 5m） |
| `log_enabled` | 是否启用日志记录（默认 false） |
| `log_requests` | 是否记录请求详情（默认 true） |
| `log_responses` | 是否记录响应详情（默认 false） |
| `data_dir` | 统计快照存放目录（默认 `data`） |
| `stats_enabled` | 是否启用定时统计（默认 true） |
| `stats_cron` | 5 段式 cron 表达式，按本地时间（默认 `0 2 * * *`，即每天 02:00） |
| `stats_on_start` | 启动后是否立即统计一次（默认 true） |
| `stats_timeout` | 单次统计的整体超时上限（默认 30m） |
| `scan_all_branches` | 提交是否覆盖**所有分支**（`all=true`）而非只取默认分支（默认 true） |
| `refresh_overlap_days` | 每次增量采集向前回溯的天数，用于捕获 amend / rebase 提交与滞后回写的 MR 状态（默认 30） |
| `backfill_chunk_days` | 历史回填的单块跨度；首次采集只回填一块，此后每次运行再向前推进一块（默认 30） |
| `exclude_authors` | 不计入贡献统计的作者标识数组（用户名 / 邮箱 / 姓名，大小写不敏感） |
| `exempt_from_inactive` | 仅从「未参与成员」名单中豁免的账号，其提交与 MR 仍照常计入其余全部统计（用户名 / 邮箱 / 姓名，大小写不敏感） |

`cache_ttl` 与 `stats_cron` 是相互独立的两个概念：前者缓存在一次统计过程中产生的原始
GitLab 接口响应，后者决定多久重建一次统计快照（即前端可见的数据刷新频率）。

#### 关于 `scan_all_branches`

只采默认分支会严重低估这类实例上的真实工作量：大量开发发生在 `bugfix/*`、`feature/*`
分支上，只通过 Merge Request 进入默认分支。某实例 30 天窗口的实测对照：

| 项目 | 默认分支 | 全分支 |
| --- | --- | --- |
| `project-a` | 0 | 14 |
| `project-b` | 1 | 680 |
| `project-c` | 0 | 953 |

开启 `all=true` 后 GitLab 会按 commit id 跨分支去重（实测去重后重复率 0.0%），
调用方无需再去重。代价在于分页数量而非正确性：`with_stats=true` 本身只带来约 1.1x 开销，
而切到全分支会让行数增长约 6.8x、耗时增长约 7.2x。

#### 关于增量采集

单个超大项目一年全分支的提交可能就要几分钟才能枚举完（实测：某项目约 170 页 /
约 1.66 万条 / 约 580 秒），因此一次性全量采集不可行，采集改为**增量**模式：

- `refresh_overlap_days`（默认 30）——每次运行都重采这段最近区间，以捕获 amend、rebase、
  cherry-pick 产生的提交以及滞后回写的 MR 状态变化。
- `backfill_chunk_days`（默认 30）——首次只采最近一块，此后每次运行再向前推进一块，
  直到覆盖满 `WindowDays` 定义的整个窗口。

快照用 `covered_from` 记录自己的覆盖进度：只要它还晚于窗口起点，面板就显示
「历史回填中」并给出已覆盖到哪一天；推进到窗口起点后即报告为完整。
无论窗口多大，每次运行的成本都是有界的——某约 450 个项目的实例实测每次约 46 秒。

合并是**按日期整体替换**：本次重新采到的日期整行替换，其余日期原样保留。
同一天的计数不会被累加两次，因此窗口增长不会导致总量虚增。

单次运行会产生**两段不相邻的区间**——最近的刷新段 + 一段更早的回填段，两者之间夹着
上次已采、本次不采的历史。因此替换范围必须按**区间集合**判定，而不是取它们的最早起点
当作唯一边界：那样会把中间那段历史一并丢弃，而本次采集根本没有它的数据。实测某实例上
曾整段 30 天从约 1700 条悄悄掉到 4 条，`covered_from` 却照常推进、对外报告为已覆盖。

因为替换单位是「一整天」，所有区间边界都对齐到自然日的整点。若区间从某天的半途开始，
那一天只会被采到一半，而 `covered_from` 却声称整天已覆盖，于是上一份快照中的整天数据
被丢弃、日期被截断——实测某实例上一度出现某天只剩 3 条，而相邻日期分别为 65 条和 9 条。

#### 关于日期归属时区

日期一律按**固定时区（东八区）**归日，不使用提交者自己的时区。GitLab 会完整保留作者
提交时的时区偏移，因此同一个时刻由美西（`-07:00`）和中国（`+08:00`）的提交者写出，
会落到相差一天的两个日期上：同一个人的产出被拆散到两天，而靠近区间边界时该记录还会
落到采集区间之外——那里的记录不在按日替换的范围内，于是与上一份快照的同日记录并存。
实测某实例上一条时间戳为 `2026-08-14T22:07:09-07:00` 的提交
（即 `2026-08-15T05:07:09Z`，明明落在刷新区间内）就被归到了前一天。

#### 关于 `exclude_authors`

GitLab 的 `bot` 标记（group bot / project bot / 服务 token）只能覆盖平台自己识别的
自动化账号。现实中还有一类账号平台不会标记，但同样是自动化身份——典型是**打包 / 发版账号**：
每次构建只改动版本字符串，因此 `additions` 与 `deletions` 高度对称、提交极频繁，
但实际代码贡献接近于零。这类账号会挤占贡献排行，且无法靠 API 自动识别，
只能由使用方显式声明：

```json
{
  "exclude_authors": ["packaging-bot", "release-bot"]
}
```

**如何识别这类账号**：看排行榜中「提交数很高、改动行数极低」的账号，并观察其
`additions` 是否长期等于 `deletions`。例如某实例中：

| 账号 | 提交数 | 总改动行数 | 增删对称率 |
| --- | --- | --- | --- |
| `release-bot` | ~1,200 | ~1,500 | 99.6% |
| `packaging-bot` | ~520 | ~1,000 | 100% |

真人的提交中增删几乎不可能长期相等。判定后把账号名（或邮箱、姓名）写入
`exclude_authors` 即可。

还有一类同样常见、表现也一致的账号：**构建 / CI 容器身份**。流水线提交时若没有配置
`git user.email`，身份会退化成 `root@<容器主机名>`，匹配不到任何 GitLab 账号，
于是以 `author_key: 0` 落盘，并在排行榜上以 `root` 之名出现：

| 身份 | 提交数 | 总改动行数 | 增删对称率 |
| --- | --- | --- | --- |
| `root@ci-runner-01` | ~3,000 | ~10,200 | 100% |
| `root@ci-runner-02` | ~270 | ~1.0M | 0.4% |

两个极端都是机器特征：完全对称意味着每次构建只改写同一对增删，而 0.4% 意味着一次性
导入了上百万行、删除却只有几千行。真人不可能产生任一种形态。注意它在页面上显示的名字
只有 `root`——请登记**完整邮箱地址**（`root@<主机名>`），不要写裸 `root`：
所有容器共用这个名字，按姓名匹配还会误伤 GitLab 管理员账号。

被排除的作者不会进入提交频率、代码量、贡献排行和 MR 统计。同一份名单同样作用于用户表，
避免被排除的账号仅因「自身没有活动」而出现在未参与成员报表里。
原始记录仍保留在快照里（快照的 `excluded_authors` 字段会记录本次生效的口径），
便于口径变化后可追溯与重算。

#### 关于 `exempt_from_inactive`

`exclude_authors` 是把账号从统计里整体拿掉——这对自动化身份合适，对人则不合适。
有些成员所处岗位本就不承担日常编码指标，典型是**管理层 / 领导、测试与产品岗**。
他们可能仍有零星提交，这些提交是真实工作、理应计入数据；不该出现的是他们的名字
被列进**未参与成员**名单。

这份名单对展示范围很敏感：它按当前选择的时间范围计算，因此一年里有几次提交的人，
一旦把范围收窄到「最近 30 天」或「最近 7 天」，就会掉进名单。用 `exempt_from_inactive`
可以把这类账号从该名单取下，同时不影响其他任何指标：

```json
{
  "exclude_authors": ["packaging-bot", "release-bot"],
  "exempt_from_inactive": ["team-lead"]
}
```

两份名单的区别只在作用范围——这正是需要同时存在的理由：

| 名单 | 提交频率 | 代码量 | 贡献排行 | MR 统计 | 未参与名单 |
| --- | --- | --- | --- | --- | --- |
| `exclude_authors` | 剔除 | 剔除 | 剔除 | 剔除 | 隐藏 |
| `exempt_from_inactive` | 计入 | 计入 | 计入 | 计入 | 隐藏 |

两者都支持填写用户名、邮箱或姓名，匹配时忽略大小写与首尾空白。本次生效的名单会记录在
快照的 `exempt_from_inactive` 字段里，便于未参与名单的构成可追溯、可重算。

#### 关于用户表

成员名单以 `active=true` 从 GitLab 读取，因此采集到的用户表只包含当前启用中的账号。
这个过滤并非形式：某实例上，它把账号列表收窄到全部账号中的少数，
其余均为 blocked / deactivated / banned。

该过滤条件同时排除了 GitLab 自身的系统账号
（`GitLabDuo`、`GitLab-Admin-Bot`、`support-bot`、`alert-bot`）与 `Ghost User` 占位账号。
这里没有实际损失——四个 bot 都是自动化身份，而 Ghost User 本就是 GitLab 用来归档
已删除账号提交的占位身份。

这个过滤是刻意保留的，它正是**离职同事不会出现在未参与成员名单里**的原因。他们的历史提交
仍会从各项目里被采集，也仍计入提交频率、代码量与贡献排行——离职者在窗口内确实做过这些工作
——但不会被当作「在职却未参与开发」点名。

若改为加载全部账号，会把所有 blocked / deactivated 账号放进用户表；
它们都没有近期活动，于是会直接灌进未参与名单。若确实要改动这个过滤条件，
请同步检查 `aggregate.go` 中的未参与成员判定——两者是按设计耦合的。

### 获取 Token

1. 登录 GitLab
2. 进入 Profile → Access Tokens
3. 生成新 Token，勾选以下权限：
   - `read_api`
   - `read_user`

### 运行

```bash
./gitlab-stats.exe
```

然后访问 http://localhost:8080

首次启动会立即执行一次统计（除非 `stats_on_start` 设为 `false`）。统计进行中面板会显示
实时进度条；统计完成后结果写入 `data/stats.json`，此后每次打开页面都直接读取该快照，
不再请求 GitLab。

## 工作原理

设计上把「采集」与「展示」彻底分离：

```
       定时触发 / 手动触发
                 │
                 ▼
        ┌─────────────────┐
        │      采集器      │  并发读取 commits + MRs（仅 GET）
        └────────┬────────┘
                 │  聚合为「按人 × 按天」的记录
                 ▼
        ┌─────────────────┐
        │ data/stats.json │  原子写入（临时文件 + rename）
        └────────┬────────┘
                 │
                 ▼
        ┌─────────────────┐
        │    HTTP 处理器   │  从内存读快照，按需即时聚合
        └─────────────────┘
```

关键特性：

- **一次采集服务全部面板。** 所有指标都从同一份「按人 × 按天」记录派生，因此提交频率、
  代码量、贡献排行、未参与成员检测、MR 趋势不需要二次拉取。
- **展示范围只是视角。** `period` / `days` 查询参数只在内存中裁剪聚合范围，切换它们不会
  触发任何 GitLab 请求。快照窗口固定为 365 天（`WindowDays`），由采集逐步回填覆盖。
- **「参与」的判定是提交或 MR 任一。** 窗口内有过提交，或参与过任何 Merge Request，
  都算参与。只看提交会误判走 MR 流程的开发者：某实例报告的「零提交成员」里
  约有一半实际在活跃，其中有人一年创建了数百个 MR。
- **身份按稳定维度匹配。** 优先级为 **GitLab 用户 ID → 邮箱 → 用户名**，姓名仅作最后兜底。
  姓名既不唯一也会被随意修改——按姓名匹配既会误并不同的人（某姓名对应 3 个不同邮箱），
  也会把同一个人因改名拆成多个身份。
- **快照写入是原子的。** 先写临时文件再 rename，写一半崩溃不会留下无法解析的
  `stats.json`。
- **失败相互隔离。** 单个项目读取失败会记录到 `project_errors` 并在面板提示，其余项目
  照常统计。
- **缺失的行数会被标记。** GitLab 对部分提交（合并提交、超大提交）不返回 `stats`，这类
  提交通常仍计入提交数，但所在记录会被标记 `stats_incomplete`，而不是静默按 0 行统计。
- **机器人、排除名单与豁免名单三者的处理各不相同。** GitLab 标记 `bot: true` 的账号
  （group bot、project bot、服务 token）以及 `exclude_authors` 中列出的账号，既不出现在
  贡献统计里，也不会出现在未参与名单里；`exempt_from_inactive` 中的账号则只从未参与名单
  取下，其提交与 MR 在其余指标里照常计入。已被 GitLab 停用的账号根本不会被采集，
  因此离职同事不会被当作未参与成员点名。
- **贡献者按稳定身份归并。** 用户 ID 跨改名与邮箱变更保持稳定，因此同一个人改名后不会被
  拆成两个贡献者，同名但属于不同账号的人也不会被错误合并。未匹配到 GitLab 用户的提交者
  （例如镜像仓库带入的上游开源作者）以 `author_key: 0` 落盘，退回邮箱归并。
- **重启安全。** 启动时先从磁盘加载快照，面板立即可用，重启不会强制重跑统计。
- **前端不会用到旧版本。** 页面响应声明 `no-store`，重新编译的二进制必定能到达浏览器。
  内嵌资源拿不到修改时间（embed 不提供 `ModTime`），因此改用内容哈希做 `ETag`，
  每次请求协商：内容未变返回 304，字节一变立刻下发新副本。

## API 接口

| 接口 | 方法 | 说明 |
|------|------|------|
| `/` | GET | Web 面板 |
| `/health` | GET | 健康检查（含快照时间与任务状态） |
| `/api/stats/status` | GET | 快照元信息 + 统计任务进度 |
| `/api/stats/refresh` | POST | 手动触发一次统计（已有任务执行中返回 409） |
| `/api/stats/commit-frequency` | GET | 提交频率统计 |
| `/api/stats/mr-statistics` | GET | MR 状态统计 |
| `/api/stats/code-volume` | GET | 代码量统计 |

**查询参数：**

- `period` - 统计周期：`day`（默认）/ `week` / `month`
- `days` - 展示天数：默认 90，取值为 (0, 快照窗口] 内的正整数，上限默认 365。
  口径为**含今天在内的 N 个自然日**——`days=7` 覆盖今天与之前 6 天。
  无下界限制，因此 7 天这类短区间同样可用。

### 状态接口响应

`GET /api/stats/status` 是面板判断「展示缓存数据」还是「展示进度条」的唯一依据：

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

`covered_from` 是快照实际覆盖到的最早日期。只要它还晚于窗口起点，面板就会提示历史数据
覆盖到了哪一天；推进到窗口起点后即为完整覆盖。详见
[关于增量采集](#关于增量采集)。

在快照尚未生成时，统计接口返回 **503**（若统计正在进行则返回 **202**），并带结构化错误
信息，面板据此区分「还没数据」与「正在加载」。

## 开发

### 项目结构

```
gitlab-stats/
├── main.go          # 入口：依赖装配、服务生命周期、优雅停机
├── config.go        # 配置加载与校验
├── types.go         # 数据结构（含容错的 Commit JSON 处理）
├── gitlab.go        # GitLab API 客户端（只读）
├── collector.go     # 并发采集并聚合成快照
├── aggregate.go     # 快照 → 接口响应体聚合
├── store.go         # 快照原子持久化
├── job.go           # 后台任务状态机与进度
├── scheduler.go     # 零依赖的 5 段式 cron 调度器
├── cache.go         # 单次采集内的原始响应缓存
├── handler.go       # HTTP 处理（只读快照，不访问 GitLab）
├── mockgitlab.go    # 独立只读模拟服务，用于冒烟测试
├── config.json      # 配置文件
├── go.mod           # Go 模块
├── static/
│   └── vendor/
│       └── chart.umd.min.js   # 内嵌的前端图表库（无需外网）
├── templates/
│   └── index.html   # Web 面板
└── *_test.go        # 测试文件
```

> `static/vendor/` 必须随仓库提交：该文件由 `main.go` 的 `go:embed static/*` 编入二进制，
> 缺失会导致编译失败。`.gitignore` 中已用 `!static/vendor/**` 对其放行。

### 前端资源说明

面板所需的前端库（Chart.js 4.4.0）通过 `go:embed` 编进可执行文件，由服务端在
`/static/` 路径下提供。这样做的原因：

- **内网可用**：部署环境通常无法访问外网 CDN，若走 CDN 会导致图表整体失效
- **单文件部署**：部署只需一个可执行文件，不必额外拷贝前端资源
- **无外部依赖**：不受第三方 CDN 可用性影响

### 运行测试

```bash
# 运行所有测试
go test -v

# 查看覆盖率
go test -cover
```

### 无 Token 冒烟测试

`mockgitlab.go` 是一个独立的只读 GitLab API 模拟服务（通过 `//go:build ignore` 排除在
正式构建之外）。它模拟 120 个项目——超过一页，因此能覆盖分页逻辑——无需真实 GitLab
实例即可验证完整链路：

```bash
# 终端 1
go run mockgitlab.go          # 监听 :9999

# 终端 2 —— 将 gitlab_url 指向 http://localhost:9999 后
go build -o gitlab-stats.exe . && ./gitlab-stats.exe
```

### 重新编译

```bash
go build -o gitlab-stats.exe
```

## 性能说明

- **并发控制** - 通过 `max_concurrent` 控制并发请求数，防止被限流
- **连接池** - HTTP 客户端配置连接复用
- **单次遍历** - 一次采集喂给全部面板，commits 只拉取一次
- **分页处理** - 自动处理 GitLab API 分页，获取完整数据
- **读取零成本** - 打开页面、切换展示范围都不会访问 GitLab
- **运行成本有界** - 增量回填让单次运行的开销与统计窗口大小无关

参考数据：在约 450 个项目、150 个活跃用户的实例上，`max_concurrent: 20` 时一次采集
约 40–46 秒，快照文件约 2.9 MB。

采集成本由 **API 分页数量**决定，而非 `with_stats`：

| 配置 | 提交数（25 项目抽样，365 天） | 耗时 | 外推至约 450 个项目 |
| --- | --- | --- | --- |
| 仅默认分支 | ~4,500 | ~120s | ~110s |
| 全分支（`all=true`） | ~30,800 | ~860s | ~780s |

`with_stats=true` 单独的实测开销约为 1.1x（某项目第 30 页：0.70s vs 0.74s）。
真正拖长尾的是最大的那批项目：单个项目就有约 170 页 / 约 1.66 万条 / 约 580 秒。
这正是不能一次性全量回填、必须分块推进的原因。

## 故障排查

### 配置加载失败

检查 `config.json` 是否存在并且格式正确。`stats_cron` 非法会在启动阶段直接报错，而不是
被静默忽略。

### GitLab API 错误

确认：
- Token 权限是否足够（`read_api`、`read_user`）
- Token 是否已过期
- GitLab URL 是否正确
- 网络连接是否正常

### 面板提示「部分项目采集失败」

有项目读取失败（常见于空仓库返回 `404 Repository Not Found`）。采集日志会列出失败项目，
`data/stats.json` 的 `totals.project_errors` 中也有记录。

### 面板提示「数据已超过 26 小时未更新」

定时统计近期没有成功。检查进程是否仍在运行、GitLab 是否可达，然后点击面板上的
「重新统计」按钮手动执行一次。

### 性能问题

调整 `max_concurrent`：

```json
{
  "max_concurrent": 50
}
```

## 许可证

本项目采用 Apache License, Version 2.0 许可证。

详见 [LICENSE](LICENSE) 文件或访问 http://www.apache.org/licenses/LICENSE-2.0 了解完整条款。
