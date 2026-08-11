# Plataforma universal de pagos con criptomonedas

Esta guía resume la visión de producto, desarrollo y operaciones de una
plataforma de pagos con criptomonedas independiente y de propósito general.

## Guía de producto

### Propósito

Un comercio crea una intención de pago. La plataforma fija una cotización,
emite una ruta de red, observa la cadena, conserva cada transferencia como un
hecho inmutable, la concilia, registra el settlement en el libro contable y
envía un evento firmado.

El comercio sigue siendo responsable de su pedido, cliente, inventario,
suscripción o saldo. La plataforma confirma el pago, pero nunca entrega el
producto del comercio.

### Alcance completo

- API servidor a servidor, checkout alojado e incrustable y enlaces de pago.
- Monedas nativas y tokens en EVM, TRON, Solana, TON y adaptadores Move
  aprobados.
- Pagos exactos, parciales, insuficientes, excedentes, tardíos, con comisión
  deducida, con activo equivocado e internos de contratos inteligentes.
- Cursores duraderos, varios RPC, políticas de finalidad y tratamiento de reorg.
- Libro contable, outbox de callbacks, historial de eventos y conciliación.
- Portal del comercio, cola de operaciones, administración y sandbox
  determinista.
- Refund/sweep opcional mediante un firmante de tesorería aislado. Una
  instalación watch-only no simula capacidades de custodia.

### Invariantes

- Un evento canónico de cadena solo puede acreditarse una vez.
- Los importes financieros no usan `float`; se almacenan en unidades mínimas o
  decimales exactos.
- La hora del bloque determina si el pago es tardío.
- La IA solo ordena candidatos deterministas; no liquida pagos.
- Toda decisión manual conserva actor, motivo, versión, evidencia y aprobaciones.
- Un reorg genera eventos compensatorios y nunca borra el historial liquidado.
- Las claves privadas no están en API, checkout, escáneres ni paneles.

### Experiencia de pago

1. El backend del comercio crea un intent con referencia opaca e importe minor.
2. Una route fija red, activo, importe raw, dirección/memo, cotización y caducidad.
3. El checkout muestra red y contrato/mint, importe neto exacto, QR, contador,
   copia segura y advertencias de red incorrecta.
4. `observed` y `confirming` actualizan la interfaz, pero no entregan el producto.
5. La finalidad y el commit del ledger crean `payment.settled`.
6. El inbox transaccional del comercio aplica ese evento una sola vez.

Pagos ambiguos, tardíos o con activo incorrecto pasan a revisión; no se pierden.
Cancelar un intent no impide observar una transferencia posterior.

## Guía para desarrolladores

### Modelo y estados

Los objetos principales son `payment_intent`, `route`, `transfer_event`,
`match/contribution`, `settlement`, `domain_event`, `delivery` y
`unmatched_case`.

```text
created → awaiting_route_selection → pending → observed → confirmed → settled
                                      │          └→ partially_paid
                                      ├→ expired → needs_review
                                      └→ cancelled
needs_review → settled | reversed
settled → overpaid
confirmed/settled/overpaid → reorg_review → settled | reversed
```

Nunca se vuelve de `settled` a `pending`. La transferencia sigue
`observed → confirmed → finalized`, con rutas explícitas `reorged` e
`invalidated`.

Un caso no conciliado sigue `new → candidates_ready → bound →
verification_requested → verified → resolved`, con ramas explícitas
`approval_required`, `verification_retry`, `conflict` y `reorged`. Aceptar un
déficit o un activo distinto requiere un segundo operador. La verificación
vuelve a leer la evidencia almacenada y la cadena; nadie introduce manualmente
el importe acreditado.

### Flujo API mínimo

```http
POST /v1/payment-intents
Idempotency-Key: pedido-2026-00042

{
  "merchant_order_id": "pedido-2026-00042",
  "amount_minor": "49900",
  "currency": "EUR",
  "currency_scale": 2,
  "customer_reference": "cliente-opaco-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

Todos los importes JSON son cadenas con scale/decimals explícitos. Toda mutación
requiere idempotencia: repetir clave y cuerpo devuelve el recurso original;
cambiar datos inmutables bajo la misma clave produce `idempotency_conflict`.

La superficie incluye intents, routes, cancelación, payment proofs como pista,
event history, transfers, reconciliation, assets y simulaciones sandbox.

### Firma de solicitudes

```text
HMAC-SHA256(secret,
  METHOD + "\n" + CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" + NONCE + "\n" + SHA256_HEX(RAW_BODY))
```

Se envían key ID, timestamp, nonce aleatorio, `Content-Digest` y firma. Las
credenciales live y sandbox son distintas; Ed25519 y mTLS están disponibles para
integraciones de mayor garantía.

### Consumidor de webhooks

Solo `payment.settled` concede normalmente el producto. El endpoint debe:

1. limitar y leer el cuerpo raw;
2. verificar key ID, timestamp, nonce/delivery ID, digest y firma antes de confiar
   en JSON;
3. comprobar merchant, entorno, pedido, importe, moneda y tipo de evento;
4. insertar `(event_id, body_digest)` en un inbox único dentro de una transacción;
5. reconocer un duplicado idéntico sin repetir el efecto;
6. responder 409 y alertar si el mismo ID llega con otro digest;
7. actualizar el pedido y escribir un fulfillment outbox en la misma transacción;
8. confirmar y devolver `acknowledged_event_id`.

Los reintentos conservan event ID y body, pero cambian delivery ID, timestamp,
nonce y firma. El orden HTTP no está garantizado. Consulte los ejemplos
ejecutables en [`../../examples`](../../examples/README.md).

## Guía de operaciones

### Despliegue y fiabilidad

- API y admin BFF sin estado detrás de ingress/WAF.
- PostgreSQL como única verdad financiera.
- Outbox transaccional y workers con lease; NATS/Redis no sustituyen el ledger.
- Indexadores por red con cursor duradero y fallback/quorum de RPC.
- Workers separados para delivery, rates, expiración y conciliación.
- Firmante de tesorería aislado y sujeto a aprobación.

### Observabilidad

Correlacione intent, route, transfer, match, settlement, event y delivery.
Supervise retraso de escáner, desacuerdo RPC, latencias hasta settlement, edad de
unmatched, backlog y dead letters de callbacks, conflictos de idempotencia,
fallos de firma/replay, diferencias de ledger, edad de cotización, reorg y
decisiones manuales.

### Incidentes, copias y lanzamiento

Pause solo el activo afectado cuando sea posible. Los runbooks cubren caída RPC,
retraso del escáner, reorg, callback caído, crecimiento unmatched, precio
anómalo, clave comprometida, diferencia contable, signer y recuperación de DB.
No se corrige historial financiero con `UPDATE` manual.

Se requieren WAL cifrado, PITR probado, restauraciones periódicas, auditoría
particionada/redactada y archivo WORM según política. Production exige pruebas de
contrato, concurrencia, duplicados, importes exactos, reorg, seguridad, i18n,
accesibilidad, carga, restore y conciliación. Véase
[`../TEST_PLAN.md`](../TEST_PLAN.md).
