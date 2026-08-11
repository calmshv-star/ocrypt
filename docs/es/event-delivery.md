# Operación de entrega de eventos con JetStream

PostgreSQL sigue siendo la fuente de verdad. JetStream solo es una ayuda
opcional de entrega al menos una vez; `GET /v1/events` recupera desde PostgreSQL
y nunca cambia silenciosamente al broker.

Active el worker outbox únicamente después de que un operador aprovisione
`MERCHANT_EVENTS_V1` con el subject fijo `merchant.events.v1`, al menos tres
réplicas, límites finitos de edad/bytes/mensajes/sobre de 1 MiB, descarte de lo
antiguo, ventana de duplicados mayor que el reintento máximo y Delete/Purge
bloqueados. Los identificadores de tenant no se incluyen en subjects.

El worker acepta solo `tls://` con TLS 1.3, CA y nombre de servidor fijados,
certificado cliente y exactamente un archivo externo de credenciales o token.
El puerto 4222 y la salud permanecen privados. Readiness valida PostgreSQL,
NATS y la política exacta. Durante una caída, mantenga las filas pendientes;
no cambie de transporte. Solo un ack del stream correcto y secuencia no nula
permite marcar publicado. Los reintentos conservan `Nats-Msg-Id=event_id`.

El consumidor pull de referencia confirma únicamente después del commit
atómico del inbox y del efecto; un duplicado es éxito. La entrega exige pruebas
reales de TLS, drift, ack perdido, backpressure, fallo de DB tras ack y
redelivery. Las pruebas locales no demuestran un clúster vivo.
