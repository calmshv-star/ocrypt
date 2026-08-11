# Retención y archivos inmutables

El worker de retención es un plano de datos de archivo que falla de forma cerrada. Nunca elimina registros de negocio. Escribe archivos deterministas firmados con Ed25519 en un bucket S3 versionado con Object Lock `COMPLIANCE`, verifica versión exacta, longitud, SHA-256 y fecha de retención, y solo después registra evidencia inmutable en PostgreSQL.

Las clases cerradas son `callback_event_body`, `published_outbox_payload` y `event_history_payload`. Los cuerpos de callback y el historial solo se archivan porque la repetición y las lecturas del comercio aún necesitan sus bytes activos. Únicamente un payload de outbox publicado puede convertirse en `retention-tombstone/v1`, y solo si existe una copia idéntica en `event_history`. Se conservan identificadores, ámbito, tipos, secuencias, tiempos, hash original y referencia del objeto.

La poda exige una política versionada vigente, antigüedad suficiente, evidencia WORM verificada, ausencia de retención legal y de dependencias de entrega/repetición, lease y fence válidos, y dos comprobaciones separadas por el periodo de gracia. La transacción SERIALIZABLE final repite todas las comprobaciones bajo el mismo bloqueo asesor usado por los legal holds.

El proceso requiere `APP_ENV=production`, `RETENTION_OBJECT_STORE=s3`, `RETENTION_WORKER_ID` único, URL de base de datos exclusiva y credenciales S3 y clave Ed25519 montadas como archivos. `/healthz` y `/readyz` son privados. Readiness falla ante esquema/permisos ausentes, Object Lock o versionado no admitidos y leases obsoletos.

La migración 000022 añade el plano de control por tenant sin DML directo. Una solicitud fija versión y fence esperados, requiere aprobación de otra persona con MFA reciente y no se activa antes de la hora programada por el reloj de la base. Callback e historial siguen siendo solo archivo; la poda de outbox conserva el segundo ciclo de gracia. No se reduce retención frente a un hold activo ni a Object Lock ya admitido.

Un legal hold entra en vigor inmediatamente con `retention:hold_create` y MFA reciente. Se valida exactamente un ámbito tenant, merchant o record sin filtrar existencia entre tenants. Liberarlo siempre requiere solicitud y un aprobador distinto tanto del solicitante como del creador. El vencimiento exige una transición explícita y auditada del worker después de `expires_at`. El navegador solo recibe referencia de caso acotada, estado, hashes, conteos y evidencia tombstone; nunca bodies, payloads, claves/versiones de objeto ni credenciales.

Los permisos exactos son `retention:read`, `retention:policy_request`, `retention:policy_approve`, `retention:hold_create` y `retention:hold_release`. `/readyz` falla de forma cerrada sin 000022 o sus capacidades. API y worker no reciben `UPDATE`/`DELETE` directo sobre fuentes o evidencia.

Tras perder una respuesta PUT, solo se acepta un HEAD inmutable que coincida exactamente. Cualquier diferencia bloquea confirmación y poda. Un rollback no puede reconstruir un payload ya convertido en tombstone; debe conservarse 000015 hasta una restauración revisada desde el archivo verificado.
