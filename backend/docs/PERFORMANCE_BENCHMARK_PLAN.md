# Performance Benchmark Plan

目标：用数据判断 Go 迁移版本相比 Python 后端是否真正提升性能，并识别瓶颈来自 Go、数据库还是第三方服务。

## 1. 对比原则

Python 与 Go 必须尽量保持相同条件：

- 相同机器规格。
- 相同数据库快照。
- 相同 Redis。
- 相同外部 provider 配置。
- 相同反向代理配置。
- 相同测试账号和数据量。
- 相同并发模型和压测脚本。

不要只压 `/health`，否则只能证明框架开销，不代表业务性能。

## 2. 指标

每个接口记录：

| 指标 | 说明 |
|---|---|
| RPS | 每秒请求数 |
| P50 | 中位延迟 |
| P95 | 95 分位延迟 |
| P99 | 99 分位延迟 |
| Error Rate | 非 2xx/3xx 或业务错误率 |
| CPU | 服务进程 CPU |
| Memory | RSS/Heap |
| DB Latency | SQL 耗时 |
| External Latency | AI/TTS/ASR/支付等外部请求耗时 |

## 3. 接口分组

### A. 框架和轻量接口

```text
GET /health
GET /api/v1/test
GET /api/v1/tools/prompt-templates
```

目的：验证 Go 基础 HTTP 开销、JSON 响应、模板扫描缓存。

### B. 认证接口

```text
POST /api/v1/auth/login
POST /api/v1/auth/refresh
GET  /api/v1/auth/me
```

目的：验证 bcrypt、JWT、DB 查询性能。

注意：登录接口 bcrypt 成本较高，不应和普通 GET 接口直接比较 RPS。

### C. 项目接口

```text
POST /api/v1/projects/
GET  /api/v1/projects/
GET  /api/v1/projects/{project_id}
PUT  /api/v1/projects/{project_id}
DELETE /api/v1/projects/{project_id}
```

目的：验证 DB CRUD、分页、排序、用户隔离。

数据量建议：

- 1 个用户 100 项目。
- 1 个用户 10,000 项目。
- 10,000 用户，每人 10 项目。

### D. Token 与统计接口

```text
GET /api/v1/token-accounts/balance
GET /api/v1/token-accounts/usage/records
GET /api/v1/token-accounts/usage/stats
GET /api/v1/admin/statistics/overview
GET /api/v1/admin/statistics/tokens/trends
```

目的：验证聚合查询、索引和分页。

### E. 外部 AI 接口

```text
POST /api/v1/tools/analyze-images
POST /api/v1/tools/ocr-images
POST /api/v1/tools/text-to-speech-with-uri
POST /api/v1/tools/generate-script-stream
POST /api/v1/tools/generate-narration-stream
POST /api/v1/tools/match-scenes-stream
```

目的：区分服务自身性能与外部 provider 耗时。

要求：埋点拆分：

```text
total_latency = local_validation + db_time + external_api_time + serialization + streaming_time
```

## 4. 推荐工具

### hey 快速压测

```bash
hey -n 10000 -c 100 http://localhost:8000/health
```

### wrk 压测 GET

```bash
wrk -t4 -c100 -d60s http://localhost:8000/health
```

### k6 业务压测

适合带登录、token、动态数据的场景。

建议目录：

```text
scripts/bench/
  k6_login.js
  k6_projects.js
  k6_token_balance.js
  k6_stream.js
```

## 5. 示例 k6：登录后查询项目

```javascript
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  scenarios: {
    steady: {
      executor: 'constant-vus',
      vus: 50,
      duration: '2m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8000';
const EMAIL = __ENV.EMAIL;
const PASSWORD = __ENV.PASSWORD;

export default function () {
  const login = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ email: EMAIL, password: PASSWORD }),
    { headers: { 'Content-Type': 'application/json' } }
  );

  check(login, { 'login ok': r => r.status === 200 });
  const token = login.json('tokens.access_token');

  const res = http.get(`${BASE_URL}/api/v1/projects/?page=1&limit=20`, {
    headers: { Authorization: `Bearer ${token}` },
  });

  check(res, {
    'projects ok': r => r.status === 200,
    'has total': r => r.json('total') !== undefined,
  });

  sleep(1);
}
```

运行：

```bash
BASE_URL=http://localhost:8000 EMAIL=test@example.com PASSWORD=password123 k6 run scripts/bench/k6_projects.js
```

## 6. 压测阶段

### 阶段 1：本地 smoke benchmark

目的：快速发现明显错误。

```text
并发：1、10、50
持续：30s
数据：少量测试数据
```

### 阶段 2：预发环境基准

目的：Python vs Go 公平对比。

```text
并发：50、100、200、500
持续：5m
数据：生产量级快照脱敏版
```

### 阶段 3：极限和稳定性测试

目的：找到系统瓶颈和退化点。

```text
逐步加压：每 5 分钟增加并发
持续 soak test：2-8 小时
关注：内存泄漏、连接泄漏、错误率上升
```

## 7. 报告模板

| 接口 | 服务 | 并发 | RPS | P50 | P95 | P99 | 错误率 | CPU | 内存 | 结论 |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| `/health` | Python | 100 | TBD | TBD | TBD | TBD | TBD | TBD | TBD |  |
| `/health` | Go | 100 | TBD | TBD | TBD | TBD | TBD | TBD | TBD |  |
| `/auth/login` | Python | 50 | TBD | TBD | TBD | TBD | TBD | TBD | TBD |  |
| `/auth/login` | Go | 50 | TBD | TBD | TBD | TBD | TBD | TBD | TBD |  |

## 8. 判断标准

Go 版本可以认为性能达标，至少应满足：

1. 非外部依赖接口 P95 不差于 Python。
2. 同资源下 CPU/内存明显低于 Python，或吞吐明显高于 Python。
3. 错误率不高于 Python。
4. DB 查询耗时没有退化。
5. 长连接 stream 接口在目标并发下稳定。
6. 无明显 goroutine、连接、内存泄漏。

## 9. 注意事项

- AI 接口单次总耗时可能主要由第三方决定，不能简单认为 Go 一定更快。
- 登录接口受 bcrypt 成本影响，应该单独看。
- 管理统计接口如果 SQL 没优化，Go 无法弥补数据库瓶颈。
- SSE 接口要同时压测连接数、断线、超时、客户端取消。
