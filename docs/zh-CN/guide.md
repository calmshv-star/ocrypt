# 通用加密货币商户平台

本文是一套独立、通用的加密货币支付平台的精简产品、开发和运维指南。

## 产品指南

### 平台解决什么问题

这是一个面向网站、应用、机器人、SaaS 和内部系统的多租户支付基础设施。
商户创建支付意图后，平台会锁定报价、生成一个或多个链上支付路由、持续扫描区块链、
保存不可变的转账事实、完成匹配、写入结算账本，并向商户发送带签名的事件。

订单、客户、库存、访问权限、订阅和余额仍由商户系统管理。平台只证明和结算付款，
不会直接发放商户提供的商品或服务。

### 完整范围

- 服务端 API、托管收银台、可嵌入收银台和支付链接；
- EVM、TRON、Solana、TON 以及通过审核的 Move 适配器上的原生币和代币转账；
- 精确、分次、少付、多付、逾期、手续费扣除、错误资产和智能合约内部转账；
- 持久扫描游标、多 RPC 服务商、终局策略以及链重组处理；
- 双重记账账本、回调 outbox、事件历史和对账；
- 商户中心、运营未匹配队列、平台管理后台和确定性沙盒；
- 可选的托管流程由隔离的资金签名器完成。仅观察部署必须明确显示不支持退款和归集。

### 不可妥协的规则

- 同一个规范化链上事件绝不能重复入账；
- 金额在财务逻辑中绝不能使用二进制浮点数；
- 是否逾期以区块时间为准，而不是扫描器发现时间；
- AI 只能对确定性候选项重新排序，不能虚构客户或执行结算；
- 人工决策必须记录操作者、原因、对象版本、证据和独立的风险审批；
- 链重组通过补偿工作流处理，已经结算的历史不能被直接改写或删除；
- 钱包私钥不得出现在 API、收银台、扫描器或管理后台服务中。

### 用户流程

1. 商户后端用自己的订单引用和精确最小货币单位金额创建支付意图；
2. 路由固定资产、网络、原始整数金额、地址或 memo、报价来源和有效期；
3. 收银台清楚显示网络、合约或 mint、实际到账金额、二维码、倒计时、复制按钮和错网警告；
4. 平台观测并确认转账。收银台可以显示进度，但商户此时不能发货；
5. 达到终局并提交账本后产生 `payment.settled`；
6. 商户的事务性 webhook inbox 只处理一次事件，并写入自己的履约 outbox。

逾期、错误资产、歧义或违反策略的转账进入审核队列，不会静默丢失。取消支付意图只会
停止正常结算，不会停止链上观测；取消后的转账仍会成为可审核案件。

## 开发者指南

### 核心对象

- **支付意图（Payment intent）：** 不可变的商业要求及商户引用；
- **路由（Route）：** 某资产和网络的版本化报价及链上收款目标；
- **转账事件（Transfer event）：** 由网络、交易、事件索引、资产和收款方标识的标准化链上事实；
- **匹配/贡献（Match/contribution）：** 一笔转账分配给某路由的可审计记录；
- **结算（Settlement）：** 双重记账账本中不可变的入账结果；
- **领域事件（Domain event）：** 带版本和租户序号、可发送给商户的事实；
- **投递（Delivery）：** 同一个规范事件正文的一次可重试传输；
- **未匹配案件（Unmatched case）：** 审核工作流，而不是可随意编辑的转账记录。

### 支付状态机

```text
created → awaiting_route_selection → pending → observed → confirmed → settled
                                      │          └→ partially_paid
                                      ├→ expired → needs_review
                                      └→ cancelled
observed/partially_paid → needs_review → settled | reversed
settled → overpaid
confirmed/settled/overpaid → reorg_review → settled | reversed
```

系统绝不会把 `settled` 直接改回 `pending`。转账终局状态按照
`observed → confirmed → finalized` 前进，并为 `reorged` 和 `invalidated`
保留明确的补偿路径。

未匹配案件按照 `new → candidates_ready → bound → verification_requested →
verified → resolved` 推进，并保留 `approval_required`、
`verification_retry`、`conflict` 和 `reorged` 分支。接受少付或跨资产付款
必须由第二名操作员批准。验证器会重新读取已保存证据和链上数据；操作员不能手工输入
入账金额。

### 最小 API 流程

```http
POST /v1/payment-intents
Idempotency-Key: order-2026-00042
Content-Type: application/json

{
  "merchant_order_id": "order-2026-00042",
  "amount_minor": "49900",
  "currency": "CNY",
  "currency_scale": 2,
  "description": "年度套餐",
  "customer_reference": "customer-opaque-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

JSON 中的金额必须是字符串，并带有明确的 scale 或 decimals。每个变更请求都使用幂等键。
同一键和同一正文返回原结果；同一键配不同不可变输入返回 `idempotency_conflict`。

主要端点包括：

- `POST/GET /v1/payment-intents`；
- `POST /v1/payment-intents/{id}/routes`；
- `POST /v1/payment-intents/{id}/cancel`；
- `POST /v1/payment-proofs`，它只提供查找线索，绝不能直接把付款改为成功；
- `GET /v1/events?after_sequence=...`，用于补拉丢失的事件；
- `GET /v1/transfers/{network}/{tx}` 和对账报告；
- 沙盒可模拟已观测、部分付款、已结算、错误资产和链重组。

### 请求认证

HMAC 配置对原始正文的字节签名：

```text
HMAC-SHA256(secret,
  METHOD + "\n" +
  CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" +
  NONCE + "\n" +
  SHA256_HEX(RAW_BODY))
```

请求携带密钥 ID、时间戳、128 位 nonce、`Content-Digest` 和签名。高安全集成可以使用
Ed25519，并可叠加 mTLS。沙盒和正式环境的凭据必须分离，并授予最小权限。

### Webhook 消费者协议

通常只有 `payment.settled` 才能触发商品或权限发放。消费者必须：

1. 限制请求大小并读取原始正文；
2. 在信任 JSON 前验证密钥 ID、时间戳、投递 ID、摘要和签名；
3. 验证商户、环境、事件类型、订单引用、金额和币种；
4. 开始数据库事务；
5. 用唯一约束写入 `(event_id, body_digest)` inbox；
6. 完全相同的重复事件返回原确认结果；
7. 同一事件 ID 对应不同摘要时返回冲突并告警；
8. 在同一事务中更新本地订单并写入履约 outbox；
9. 提交事务并返回 `acknowledged_event_id`。

每次重试保持相同的事件 ID 和规范正文，但使用新的投递 ID、时间戳和签名。HTTP 投递
不保证顺序；租户序号和事件历史用于恢复和对账。可运行的客户端与 webhook 示例位于
[`../../examples`](../../examples/README.md)。

## 运维指南

### 部署模型

- 入口网关/WAF 后运行无状态 API 和管理 BFF 副本；
- PostgreSQL 是财务事实来源，必要时按时间或租户分区；
- 使用事务 outbox 和带租约的 worker；NATS/Redis 只辅助投递，不保存财务真相；
- 每个网络有独立索引器、持久游标以及多服务商仲裁和故障切换；
- 回调投递、汇率、对账和过期处理由独立 worker 执行；
- 签名器与公网隔离，并执行明确的审批规则。

### 必需的遥测

每个支付意图、路由、转账、匹配、结算、事件和投递都携带 trace/correlation ID。
重点监控扫描延迟和 RPC 分歧、观测到终局及结算的耗时、未匹配队列年龄、回调积压与
死信、幂等冲突、签名和重放失败、账本对账差异、报价新鲜度、地址池容量、链重组、
人工覆盖、退款以及签名器操作。

### 事故、恢复和上线门槛

应尽量只暂停受影响的资产或路由。Runbook 必须覆盖 RPC 故障、扫描延迟、链重组、
回调故障、未匹配堆积、汇率异常、密钥泄露、账本不平、签名器故障和数据库恢复。
不得用临时 SQL 修改财务历史，只能通过版本化配置和补偿账本/领域事件修复。

备份需要加密连续 WAL、经过演练的时间点恢复、证据和报告对象存储，以及定期在隔离环境
执行恢复。审计数据应按策略分区、脱敏和保留，并在需要时导出到 WORM 存储。

上线前必须通过合约、并发、重复事件、精确金额、重组、未匹配、安全、国际化、无障碍、
负载、恢复和对账测试，详见 [`../TEST_PLAN.md`](../TEST_PLAN.md)。任何跳过的 P0
不变量测试都应阻止正式发布。
