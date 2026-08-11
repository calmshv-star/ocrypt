# 汇率运行时运维

Rate worker 是 fail-closed 数据平面，只通过 `ActiveSnapshotReader` 读取已激活且不可变的 `rate_policy` 与 `rate_source`；draft、approved、scheduled 均不会生效。写入前会在锁内再次核对 snapshot ID 和 fence token，配置并发切换会使整次采集回滚。

Policy 必须包含 `base_asset`、`quote_asset`、至少两个不重复的 `sources`、`quorum`、`max_age_seconds`、`max_spread_bps`；可选 `future_tolerance_seconds`（默认 5，显式 0 表示零容忍）和 `poll_interval_seconds`（默认 15）。Source 包含 `provider_ref`、标准化 HTTPS `endpoint`、相同资产对、freshness、timeout/size 限制及可选 `credential_ref`。URL 不允许 credentials、query 或 fragment。

响应遵循 `contracts/rate-provider-v1.schema.json`。价格只能是 uint256 正整数字符串 numerator/denominator，不能使用 JSON number/float；偶数来源保守选择较低的真实中位数，不做平均或舍入。资产对、时效、未来时间、不同来源 quorum 与精确 spread 任一失败时都不会产生 tick。

禁止 redirect、proxy、私网/loopback/link-local/reserved 及公私混合 DNS；每次连接固定 DNS，限制 TLS、timeout、content type 和响应大小。Secret 仅从只读 `RATE_SECRET_DIR` 按 `credential_ref` 读取，禁止路径/符号链接逃逸和多行内容。

必需 env：`DATABASE_URL`、UUID `RATE_WORKER_ID`、严格的全局 `RATE_TARGETS_JSON`；当前拒绝 tenant target，`base_asset` 必须是 active `assets.id`，`quote_asset` 必须是三字符 fiat。可选：`RATE_SECRET_DIR`、`RATE_POLL_INTERVAL=5s`、`RATE_LEASE_DURATION=30s`、`RATE_MAX_ATTEMPTS=8`、`RATE_MAX_READY_AGE=2m`、`RATE_HEALTH_ADDRESS=:9092`。迁移 000007 前创建 NOLOGIN `rate_runtime_worker` 角色（否则之后补发 grants），workload login 继承它，并由运维创建同 UUID 的 enabled identity。

`/healthz` 检查数据库；`/readyz` 仅在所有目标均有未过期的新 tick 且无 dead-letter 时就绪。成功事务同时保存不可变 admission/provenance，并将同一 tick 投影到 `PersistedPlanner` 读取的旧 `asset_rate_ticks`；unique pair 与稳定 policy binding 防止歧义，stale/divergent 数据不可选择。
