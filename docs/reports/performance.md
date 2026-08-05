# MVP 本地非模型 API 性能报告

- 生成时间：`2026-08-05T14:48:50.131102+00:00`
- 目标：`http://127.0.0.1:18080`
- 模式：`sequential-local`
- 每端点样本：100
- P95 门槛：`< 300 ms`

| Endpoint | Samples | Mean | P50 | P95 | P99 | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| `GET /healthz` | 100 | 2.34 ms | 0.63 ms | 15.05 ms | 25.11 ms | PASS |
| `GET /readyz` | 100 | 5.03 ms | 1.38 ms | 20.74 ms | 27.06 ms | PASS |
| `GET /api/v1/admin/knowledge-bases` | 100 | 5.38 ms | 1.42 ms | 24.84 ms | 26.58 ms | PASS |

该报告只覆盖本地非模型 API，不把模型或网络 Provider 延迟混入 NFR-004。
