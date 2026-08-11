# JSON-MD5/Form-MD5 迁移

旧版适配器是临时且默认关闭的桥接层。它只通过核心 API 创建普通 payment intent/route、读取 PostgreSQL 中的状态，并仅在规范 `payment.settled` 事件后发送旧版回调。它不能把订单标记为已支付，也不能写入账本。

申请准入前必须应用迁移 `000018_legacy_compatibility`。

启用前，创建具有 `payments:read`、`payments:write`、`events:read` 权限的核心 HMAC 密钥；以只读文件挂载 HMAC/MD5 密钥；回调必须使用 443 端口 HTTPS；每个 currency/token/network 只能映射到一个核心路由。两名不同操作员必须在 30 分钟内分别申请和批准准入。

签名内容为排序后的非空 `key=value` 加密钥；JSON-MD5 排除 `signature`，Form-MD5 排除 `sign` 和 `sign_type`。请把 128 位 `trade_id` 当作机密。状态轮询只用于恢复。业务入账必须幂等，确认响应只能是小写 `ok` 或 `success`。

请在 `Sunset` 日期前迁移到核心 HMAC API 和规范 webhook。准入过期、事件序列缺口、密钥缺失或 TLS 失败都会使 readiness 关闭。本仓库不声称已完成线上 PostgreSQL、Kubernetes 或回调验证。
