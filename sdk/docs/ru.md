# Быстрый старт SDK Merchant Platform

[English](../README.md) · [简体中文](zh-CN.md) · [Español](es.md) · [Français](fr.md) · [Deutsch](de.md) · [Русский](ru.md)

Клиенты TypeScript, Python, Go, PHP, Java и .NET используют одинаковые правила HMAC, идемпотентности и webhook. Денежные суммы и атомарные единицы блокчейна всегда остаются строками с целым десятичным числом — их нельзя преобразовывать во float.

```ts
const client = new MerchantClient({ baseUrl, keyId, secret, timeoutMs: 10_000 });
const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42", amount_minor: "49900",
  currency: "USD", currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42-create");
```

Для каждой мутации нужен уникальный ключ идемпотентности. Повтор запроса с тем же ключом обязан содержать абсолютно то же тело. SDK требуют HTTPS, кроме локальной разработки, ограничивают время запроса и никогда не пишут секреты в логи или ошибки.

Webhook проверяется по неизменённым исходным HTTP-байтам до разбора JSON. Обычно только `payment.settled` разрешает выдачу товара. Реализация `WebhookInbox` должна в одной транзакции зарегистрировать событие, изменить заказ и поставить выдачу в очередь; одинаковый ID события с другим digest является конфликтом.

`payment.observed`, `payment.confirming` и `payment.reorged` до settlement содержат ограниченный объект `observation` с каноническим переводом, текущими/требуемыми подтверждениями, finality и digest доказательства. `payment.resolution.updated` содержит безопасный объект `resolution`. Эти события информационные и не разрешают выдачу.

Стабильная поверхность включает expire/metadata, payment proofs, события по `after_sequence`, детали event/transfer/quote, balances, отчёты сверки и payment links/checkout. Для отчётов нужен отдельный scope `reconciliation:read`; до разбора JSONL проверяются размер, SHA-256, зафиксированный key ID и Ed25519-подпись. Admin, operator и treasury намеренно не входят в merchant-клиент. См. [английский индекс](../README.md) и [русское руководство](../../docs/ru/api-integration.md).
