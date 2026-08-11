# Inicio rápido de los SDK de Merchant Platform

[English](../README.md) · [简体中文](zh-CN.md) · [Español](es.md) · [Français](fr.md) · [Deutsch](de.md) · [Русский](ru.md)

Los clientes de TypeScript, Python, Go, PHP, Java y .NET comparten las mismas reglas de HMAC, idempotencia y webhooks. Los importes y las unidades atómicas de cadena siempre son cadenas de enteros decimales; nunca deben convertirse a coma flotante.

```ts
const client = new MerchantClient({ baseUrl, keyId, secret, timeoutMs: 10_000 });
const intent = await client.createPaymentIntent({
  merchant_order_id: "order-2026-42", amount_minor: "49900",
  currency: "USD", currency_scale: 2,
  allowed_routes: [{ provider: "on_chain", chain_id: "tron:mainnet", asset_id: "usdt-tron" }]
}, "order-2026-42-create");
```

Cada mutación exige una clave de idempotencia única; una reintento con la misma clave debe usar exactamente el mismo cuerpo. Los SDK exigen HTTPS salvo en desarrollo local, aplican tiempos de espera y nunca registran ni incluyen secretos en errores.

Verifica el cuerpo HTTP original del webhook antes de interpretar JSON. Normalmente solo `payment.settled` autoriza la entrega. La implementación de `WebhookInbox` debe registrar el evento, actualizar el pedido y encolar la entrega dentro de una única transacción; un mismo ID con otro resumen es un conflicto.

Antes del settlement, `payment.observed`, `payment.confirming` y `payment.reorged` incluyen un objeto `observation` acotado con la transferencia canónica, confirmaciones actuales/requeridas, finalidad y resumen de evidencia. `payment.resolution.updated` incluye un objeto `resolution` sin secretos. Son eventos informativos y no autorizan la entrega.

La cobertura estable incluye expire/metadata, payment proofs, eventos con `after_sequence`, detalle de eventos/transferencias/cotizaciones, balances, informes de conciliación y payment links/checkout. Los informes exigen el scope separado `reconciliation:read`; antes de leer JSONL se verifican tamaño, SHA-256, key ID fijado y firma Ed25519. Admin, operator y treasury se excluyen deliberadamente del cliente merchant. Consulta el [índice en inglés](../README.md) y la [guía en español](../../docs/es/api-integration.md).
