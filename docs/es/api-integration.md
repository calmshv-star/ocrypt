# Guía de integración de la API

## Límite de confianza y credenciales

Use un cliente HMAC distinto por servicio y entorno. Un backend de pagos suele necesitar `payments:write`, `payments:read` y `events:read`; las exportaciones deben usar una credencial separada con `reconciliation:read`. Añada `payment-links:read`, `payment-links:write` o `checkout:write` solo cuando corresponda. Nunca exponga la clave en navegador, móvil, bot, URL, log o soporte.

Las solicitudes merchant van al origen API; los aliases de payment-link/checkout, al origen management/gateway. Los tokens públicos `pl_…` y `cs_…` son capabilities bearer de alta entropía, limitadas por tiempo, acción o número de usos.

## Monedas de factura y tipos de cambio

La moneda de la factura no está fijada a RUB. `currency` contiene exactamente tres letras ASCII mayúsculas y debe usar un código ISO 4217; `currency_scale` declara de forma explícita los decimales. `RUB`, `USD`, `EUR`, `KZT`, `INR` y `CNY` suelen usar escala `2`, por lo que `amount_minor: "3813"` representa `38,13` en la moneda elegida. La API no deduce la escala del código.

Aceptar un código no crea por sí solo una cotización cripto. Antes de crear una ruta on-chain o hosted, producción necesita un tipo admitido y reciente para el par exacto `asset_id`/moneda. Un tipo ausente, caducado, sin quorum, futuro o excesivamente divergente falla de forma cerrada; configure y admita fuentes normalizadas independientes para cada moneda de venta.

## Ciclo del pago

1. Cree un intent con `merchant_order_id` único, `amount_minor` como cadena exacta, escala, caducidad y rutas permitidas.
2. Cuando el cliente elija red/activo, cree el route y guarde importe atómico, dirección/memo, caducidad de cotización y `grace_ends_at`.
3. Muestre solo los datos devueltos. Un recibo o payment proof es una pista de búsqueda, no una liquidación.
4. Verifique y procese el webhook de forma durable. Entregue normalmente solo con `payment.settled`, de manera idempotente.
5. Repare huecos con `GET /v1/events?after_sequence=N`; la entrega puede duplicarse o desordenarse.

Cancel/expire no borran la historia on-chain. Hasta terminar la ventana grace, una transferencia tardía todavía puede acabar en review o settlement; no reutilice la dirección inmediatamente. Metadata solo reemplaza campos no financieros permitidos y exige `expected_version`; tras `409`, vuelva a leer y decida de nuevo.

## Firma, reintentos y webhooks

Firme los bytes exactos y el path/query canónico; cada nonce se usa una vez. Reintente errores de transporte, `429` y `5xx` admitidos con backoff exponencial y jitter, conservando body e idempotency key. No reintente automáticamente errores de validación o versión.

Verifique el webhook antes de parsear JSON: digest, tiempo, event ID, key ID y HMAC de `<event-id>.<timestamp>.<raw-body>`. Durante rotación conserve ambas claves. En una transacción reclame `(event_id, body_digest)`, actualice pedido, escriba fulfillment outbox y confirme; después responda el acknowledgement. El mismo ID con digest distinto es un incidente.

## Payment links, checkout y conciliación

Un payment link contiene actualmente una ruta. `public_url` solo aparece al crear o en replay exacto; list/get no revelan el secreto. Redeem consume un uso y crea intent/quote/address/route de forma atómica, entregando `cs_…`. Las return URLs son fijas. Vincule checkout embebido a un HTTPS Origin exacto y derive explorer URLs de una allowlist propia.

Los informes cubren como máximo 366 días y no admiten futuro. En estado `ready`, verifique tamaño, SHA-256, `signing_key_id` fijado y firma Ed25519 antes de leer JSONL. Conserve claves públicas antiguas durante toda la retención. El header fija ledger sequence/cutoff global y el footer contiene totales exactos como cadenas.

## Sandbox y producción

Pruebe exact, partial, over, late, wrong asset, duplicate delivery, settle-then-reorg, huecos de eventos, rotación de claves y recuperación tras timeout. Superar sandbox no autoriza producción: faltan proveedores reales independientes, pruebas finality/reorg, restore, imágenes fijadas, rotación y carga.

Team/settings es una API separada del panel y no forma parte del SDK HMAC. El contrato backend existe, pero BFF/navegador sigue pre-release; consulte [configuración del equipo](merchant-team-settings.md) tras su entrega.

Los adaptadores transaccionales para FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, Telegram y comercio genérico están en el [índice de skeletons](../../examples/frameworks/README.md). Son plantillas, no dependencias instaladas.
