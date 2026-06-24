# GitLab 统计工具

一个高性能的 GitLab 代码统计分析工具，支持提交频率、MR 状态、代码量贡献分析等功能。

[English Documentation](README.md)

## 功能特性

- 📈 **提交频率统计** - 按日/周/月统计代码提交趋势
- 🔄 **MR 状态分析** - 合并请求状态统计和趋势
- 👥 **贡献者排行榜** - 按提交次数和代码量排名
- 🔍 **零提交检测** - 找出零提交的团队成员
- ⚡ **并发处理** - 高性能并发请求，数据获取更快
- 💾 **智能缓存** - 自动缓存 API 响应，减少重复请求
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
  "gitlab_url": "https://gitlab.com",
  "token": "your-gitlab-access-token",
  "port": 8080,
  "max_concurrent": 20,
  "request_timeout": "30s",
  "cache_enabled": true,
  "cache_ttl": "5m",
  "log_enabled": false,
  "log_requests": true,
  "log_responses": false
}
```

**配置说明：**
- `gitlab_url` - GitLab 实例地址（必填）
- `token` - GitLab 访问 Token（必填）
- `port` - Web 服务端口（默认 8080）
- `max_concurrent` - 最大并发请求数（1-100，默认 20）
- `request_timeout` - 单请求超时时间（默认 30s）
- `cache_enabled` - 是否启用缓存（默认 true）
- `cache_ttl` - 缓存过期时间（默认 5m）
- `log_enabled` - 是否启用日志记录（默认 false）
- `log_requests` - 是否记录请求详情（默认 true）
- `log_responses` - 是否记录响应详情（默认 false）

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

## API 接口

| 接口 | 说明 |
|------|------|
| `GET /` | Web 面板 |
| `GET /health` | 健康检查 |
| `GET /api/stats/commit-frequency?period=day&days=90` | 提交频率统计 |
| `GET /api/stats/mr-statistics?period=day&days=90` | MR 状态统计 |
| `GET /api/stats/code-volume?days=90` | 代码量统计 |

**查询参数：**
- `period` - 统计周期：`day`（默认）/ `week` / `month`
- `days` - 统计天数：默认 90

## 开发

### 项目结构

```
gitlab-stats/
├── main.go          # 入口文件
├── config.go        # 配置加载
├── types.go         # 数据结构
├── gitlab.go        # GitLab API 客户端
├── cache.go         # 缓存实现
├── handler.go       # HTTP 处理
├── config.json      # 配置文件
├── go.mod           # Go 模块
├── templates/
│   └── index.html   # Web 面板
└── *_test.go        # 测试文件
```

### 运行测试

```bash
# 运行所有测试
go test -v

# 查看覆盖率
go test -cover
```

### 重新编译

```bash
go build -o gitlab-stats.exe
```

## 性能优化

- **并发控制** - 通过 `max_concurrent` 控制并发请求数，防止被限流
- **连接池** - HTTP 客户端配置连接复用
- **智能缓存** - 可配置的响应缓存，减少重复请求
- **分页处理** - 自动处理 GitLab API 分页，获取完整数据

## 故障排查

### 配置加载失败

检查 `config.json` 是否存在并且格式正确。

### GitLab API 错误

确认：
- Token 权限是否足够
- GitLab URL 是否正确
- 网络连接是否正常

### 性能问题

调整 `max_concurrent` 或启用缓存：

```json
{
  "max_concurrent": 50,
  "cache_enabled": true,
  "cache_ttl": "10m"
}
```

## 许可证

本项目采用 Apache License, Version 2.0 许可证。

详见 [LICENSE](LICENSE) 文件或访问 http://www.apache.org/licenses/LICENSE-2.0 了解完整条款。
