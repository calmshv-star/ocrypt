# 财务操作

本子系统提供按租户隔离的资金归集、已验证退款和确定性对账。金额必须是原子单位的规范 uint256 字符串，禁止浮点数。

## 安全模型

所有写入都在 PostgreSQL `SERIALIZABLE` 事务和强制租户 RLS 下完成。幂等键被锁定并绑定 SHA-256 请求指纹。聚合状态、预留、平衡复式分录、哈希链审计和 outbox 原子提交。源地址/nonce、日限额和可退款 settlement 均在锁内预留。链上观察到的发送者不等于所有权证明；默认只能退回独立验证的原地址。审批要求有效 step-up，且审批人与创建人必须不同。

## 执行隔离

Finalized 转账只能证明付款，不能证明地址所有权。Refund 保持 fail-closed，直到单独准入的钱包签名/托管方 verifier 写入 `financial_verified_refund_destinations`；系统绝不会自动信任 CEX、合约、GasFree 或 hot-wallet 发送者。

操作员 API 没有 build/sign/broadcast 路由。`financial-worker` 使用 fencing token 每次推进一个阶段。Builder、Signer、Broadcaster、独立 finality verifier 和 event sink 必须使用五个不同的 HTTPS origin 与凭证。禁止重定向和环境代理。每个外部副作用都带稳定幂等键和聚合绑定。平台不保存任何链私钥。

Finality 必须来自单独准入的 quorum verifier，而不是 signer/broadcaster。退款 finalized/reorg 会生成不可变的平衡/冲销分录。Outbox 使用 `SKIP LOCKED`、单调 lease token、重试、20 次后 dead-letter，以及回显相同 event ID 的确认。

## API 与运维

参见 `contracts/financial-openapi.yaml`。IAM proxy 对 tenant、actor、排序后的 permissions、有限 step-up、timestamp、nonce、path/query 和 body digest 签名。`financial_proxy_nonces` 与 merchant API client 无关。读取需要 `financial:read`。

API 必须配置数据库、TLS 和 `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`。Worker 必须配置明确的 tenant UUID 列表，以及相互独立的 `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}`。Kubernetes Pod probe 可绑定 `:9093`，但不得创建公网 Service。

审计通过 `append_financial_audit` 按租户形成 SHA-256 链；应把最新哈希定期锚定到 PostgreSQL 外部。启用资金操作前必须完成 migration up/down/up、最小权限、双租户 RLS、KMS/HSM/MPC 密钥仪式、provider 准入，以及丢失响应、stale fence、reorg、dead-letter、备份恢复和审计链验证。

## 管理端财务操作台

浏览器只访问同源 Admin BFF。BFF 根据数据库中的当前角色绑定生成租户级封闭权限集合，并拒绝商户级角色和浏览器提交的任何权限。资金操作员可申请或取消归集与退款，并申请对账；另一名高级审批人可独立批准并执行对账。客服和支付操作员不获得财务权限。

每个变更都必须携带 CSRF、精确 Origin、当前版本、原因和 `Idempotency-Key`。决策重放与聚合、审计和 outbox 变更在同一事务中保存；同一键若更换方法、路径或请求体会产生冲突。审批还要求近期 MFA，且审批人不能是创建人。原子金额在 UI 和 API 中始终保持字符串。

BFF 仅通过 TLS 1.3 连接私有财务 API，并固定 CA、显式服务器名和已验证的客户端证书。监控使用独立的最小权限客户端证书；健康端点不会降级为明文。系统禁止重定向和环境代理。浏览器不会获得内部地址、断言密钥或 custody 数据，BFF 也不公开 build/sign/broadcast/资金执行路由。实时 custody 仍为禁用且 fail-closed；操作台上线不代表 signer 或 provider 已通过生产准入。
