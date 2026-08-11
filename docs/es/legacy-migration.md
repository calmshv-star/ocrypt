# Migración de JSON-MD5/Form-MD5

El adaptador heredado es un puente temporal y desactivado por defecto. Crea intents/rutas normales en el núcleo, consulta su estado mediante la API respaldada por PostgreSQL y solo envía callbacks heredados después de `payment.settled`. No puede marcar pagos ni escribir en el libro mayor.

Aplique la migración `000018_legacy_compatibility` antes de solicitar la admisión.

Antes de admitirlo, cree una clave HMAC con `payments:read`, `payments:write`, `events:read`, monte los secretos HMAC/MD5 como archivos, use callback HTTPS en el puerto 443 y defina una asignación única moneda/token/red. Dos identidades operativas distintas solicitan y aprueban la admisión de 30 minutos.

La firma concatena `key=value` no vacíos ordenados y el secreto; JSON-MD5 omite `signature`, Form-MD5 omite `sign` y `sign_type`. Proteja el `trade_id` de 128 bits. El sondeo de estado es solo recuperación. El crédito de negocio debe ser idempotente y la respuesta exactamente `ok` o `success` en minúsculas.

Migre a HMAC y webhooks canónicos antes de `Sunset`. Admisión caducada, hueco de secuencia, secreto ausente o fallo TLS cierran readiness. No se afirma ninguna prueba live.
