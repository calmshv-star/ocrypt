# API 集成指南

## 信任边界与凭据

每个服务、每个环境使用独立 HMAC 客户端。支付后端通常需要 `payments:write`、`payments:read`、`events:read`；对账导出应使用独立的 `reconciliation:read` 凭据。仅在需要时增加 `payment-links:read`、`payment-links:write`、`checkout:write`。不得把密钥放入浏览器、移动端、机器人用户界面、URL、日志或工单。

商户请求发送到 API origin，payment-link/checkout alias 发送到 management/gateway origin。公开的 `pl_…` 与 `cs_…` 是高熵 bearer capability，并受有效期、动作或使用次数限制。

## 账单币种与汇率

账单币种并未写死为 RUB。`currency` 必须是三个大写 ASCII 字母，并应使用 ISO 4217 代码；`currency_scale` 明确指定小数位。`RUB`、`USD`、`EUR`、`KZT`、`INR` 和 `CNY` 通常使用 scale `2`，因此 `amount_minor: "3813"` 表示所选币种的 `38.13`。API 不会根据代码自动推断 scale。

接受币种代码本身并不代表已有可用的加密货币报价。创建 on-chain 或 hosted route 前，生产环境必须为精确的 `asset_id`/币种对提供新鲜且已准入的汇率。缺失、过期、未达到 quorum、来自未来或价差过大的汇率都会 fail closed；每个销售币种都必须配置并准入独立的标准化汇率源。

## 支付流程

1. 以唯一 `merchant_order_id`、字符串 `amount_minor`、币种 scale、过期时间和允许路线创建 intent。
2. 用户选择网络/资产后创建 route，并保存原子金额、地址/memo、报价过期时间和 `grace_ends_at`。
3. 只展示 API 返回的 route。钱包截图或 payment proof 只是检索提示，不是结算证明。
4. 验证并持久处理 webhook；通常仅在 `payment.settled` 后幂等交付商品。
5. 使用 `GET /v1/events?after_sequence=N` 修复缺口；投递可能重复或乱序。

Cancel/expire 不会删除链上历史。grace 窗口结束前仍会匹配迟到转账，因此可能进入 review 或后续 settlement；地址不能立即复用。Metadata 更新仅限非财务白名单字段，并要求 `expected_version`；遇到 `409` 后重新读取并重新决策。

## 签名、重试与 webhook

对一次性序列化后的原始字节及规范 path/query 签名；nonce 只能使用一次。网络错误、`429` 和允许的 `5xx` 使用带 jitter 的指数退避，并保持相同 body 与 idempotency key。不要自动重试校验或版本冲突。

解析 JSON 前验证原始 webhook：digest、时间、event ID、key ID，以及 `<event-id>.<timestamp>.<raw-body>` 的 HMAC。轮换期间按 key ID 同时保留新旧密钥。在一个数据库事务中占用 `(event_id, body_digest)`、更新订单、写 fulfillment outbox 并提交，然后才返回 acknowledgement。同一 ID 出现不同 digest 必须按安全事件处理。

## Payment link、checkout 与对账

当前 payment link 恰好绑定一条 route。`public_url` 仅在创建或完全相同的 replay 中返回，list/get 不泄露 bearer secret。Redeem 原子地消耗一次使用、创建 intent/quote/address/route 并签发 `cs_…`。返回 URL 由商户固定，浏览器不可覆盖。Embedded token 必须绑定精确 HTTPS Origin；explorer URL 应由可信网络白名单生成。

审计报告最多覆盖 366 天且不能包含未来时间。状态为 `ready` 后，在解析 JSONL 前验证长度、SHA-256、固定 `signing_key_id` 和 Ed25519 签名。历史公钥必须保留整个报告保留期。Header 固定全局 ledger sequence/cutoff，footer 使用精确字符串 totals。

## Sandbox 与上线

测试 exact、partial、over、late、wrong asset、duplicate delivery、settle-then-reorg、事件缺口、密钥轮换和超时恢复。Sandbox 通过不等于生产准入；仍需真实双 provider、finality/reorg、数据库恢复、镜像固定、密钥轮换及负载证据。

Team/settings 是独立的商户后台 API，不属于 HMAC SDK。后端合同已存在，但 BFF/浏览器启用仍为预发布；组件交付后参阅[团队设置](merchant-team-settings.md)。

FastAPI/Django、Laravel/Symfony、Express/NestJS、Spring Boot、ASP.NET、Telegram 与通用电商的事务适配模板见[框架示例索引](../../examples/frameworks/README.md)。它们是适配骨架，不会安装框架依赖。
