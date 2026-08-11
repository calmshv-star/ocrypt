# Merchant Platform SDK 快速开始

[English](../README.md) · [简体中文](zh-CN.md) · [Español](es.md) · [Français](fr.md) · [Deutsch](de.md) · [Русский](ru.md)

TypeScript、Python、Go、PHP、Java 和 .NET 客户端实现同一套 HMAC、幂等与 webhook 规则。金额和链上原子单位始终是十进制整数字符串，绝不能转换成浮点数。

```ts
const client = new MerchantClient({ baseUrl, keyId, secret, timeoutMs: 10_000 });
const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42", amount_minor: "49900",
  currency: "USD", currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42-create");
```

每个写操作都需要唯一的幂等键；同一个键只能与完全相同的请求体重试。SDK 只接受 HTTPS（本机开发除外），设置超时，不记录密钥，也不会在异常中泄露密钥。

Webhook 必须使用未经修改的原始 HTTP 字节验证，然后再解析 JSON。只有 `payment.settled` 通常可以触发交付。`WebhookInbox` 的实现必须在同一数据库事务内登记事件、更新订单并加入交付任务；相同事件 ID 与不同摘要必须作为冲突报警。

结算前的 `payment.observed`、`payment.confirming` 和 `payment.reorged` 包含受限的 `observation` 对象：规范化转账、当前/所需确认数、终局状态和证据摘要。`payment.resolution.updated` 只包含无秘密的 `resolution` 对象。这些事件仅供参考，不能触发交付。

稳定接口包括 expire/metadata、payment proof、基于 `after_sequence` 的事件恢复、event/transfer/quote 详情、balances、签名对账报告以及 payment link/checkout。报告必须使用独立的 `reconciliation:read` scope，并在解析 JSONL 前验证长度、SHA-256、固定 key ID 和 Ed25519 签名。Admin、operator、treasury 有意不放入 merchant 客户端。参阅[英文索引](../README.md)及[中文指南](../../docs/zh-CN/api-integration.md)。
