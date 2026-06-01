# GitLab Stats

A high-performance GitLab code statistics and analysis tool that supports commit frequency, MR status, and code volume contribution analysis.

[中文版文档](README-CN.md)

## Features

- 📈 **Commit Frequency Statistics** - Analyze code commit trends by day/week/month
- 🔄 **MR Status Analysis** - Merge request status statistics and trends
- 👥 **Contributor Leaderboard** - Rank contributors by commit count and code volume
- 🔍 **Zero-Commit Detection** - Identify team members with zero commits
- ⚡ **Concurrent Processing** - High-performance concurrent requests for faster data retrieval
- 💾 **Smart Caching** - Automatically cache API responses to reduce duplicate requests
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
  "gitlab_url": "https://gitlab.com",
  "token": "your-gitlab-access-token",
  "port": 8080,
  "max_concurrent": 20,
  "request_timeout": "30s",
  "cache_enabled": true,
  "cache_ttl": "5m"
}
```

**Configuration Options:**
- `gitlab_url` - GitLab instance URL (required)
- `token` - GitLab access token (required)
- `port` - Web service port (default: 8080)
- `max_concurrent` - Maximum concurrent requests (1-100, default: 20)
- `request_timeout` - Single request timeout (default: 30s)
- `cache_enabled` - Enable caching (default: true)
- `cache_ttl` - Cache expiration time (default: 5m)

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

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Web dashboard |
| `GET /health` | Health check |
| `GET /api/stats/commit-frequency?period=day&days=90` | Commit frequency statistics |
| `GET /api/stats/mr-statistics?period=day&days=90` | MR status statistics |
| `GET /api/stats/code-volume?days=90` | Code volume statistics |

**Query Parameters:**
- `period` - Statistics period: `day` (default) / `week` / `month`
- `days` - Number of days to analyze: default 90

## Development

### Project Structure

```
gitlab-stats/
├── main.go          # Entry point
├── config.go        # Configuration loading
├── types.go         # Data structures
├── gitlab.go        # GitLab API client
├── cache.go         # Cache implementation
├── handler.go       # HTTP handlers
├── config.json      # Configuration file
├── go.mod           # Go module
├── templates/
│   └── index.html   # Web dashboard
└── *_test.go        # Test files
```

### Running Tests

```bash
# Run all tests
go test -v

# View coverage
go test -cover
```

### Rebuilding

```bash
go build -o gitlab-stats
```

## Performance Optimization

- **Concurrency Control** - Control concurrent requests via `max_concurrent` to prevent rate limiting
- **Connection Pooling** - HTTP client configured for connection reuse
- **Smart Caching** - Configurable response caching to reduce duplicate requests
- **Pagination Handling** - Automatic GitLab API pagination for complete data retrieval

## Troubleshooting

### Configuration Loading Failed

Check if `config.json` exists and has the correct format.

### GitLab API Errors

Verify:
- Token has sufficient permissions
- GitLab URL is correct
- Network connection is working

### Performance Issues

Adjust `max_concurrent` or enable caching:

```json
{
  "max_concurrent": 50,
  "cache_enabled": true,
  "cache_ttl": "10m"
}
```

## License

This project is licensed under the Apache License, Version 2.0.

See the [LICENSE](LICENSE) file or visit http://www.apache.org/licenses/LICENSE-2.0 for full terms.
