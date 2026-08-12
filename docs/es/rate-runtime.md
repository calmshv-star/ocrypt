# Operación del runtime de tipos

El rate worker es fail-closed: solo consume snapshots activos e inmutables `rate_policy` y `rate_source` mediante `ActiveSnapshotReader`. Nunca usa draft/approved/scheduled. Antes del commit vuelve a comprobar snapshot ID y fence token bajo bloqueo; una activación concurrente revierte toda la recolección.

La policy exige `base_asset`, `quote_asset`, al menos dos `sources` distintos, `quorum`, `max_age_seconds` y `max_spread_bps`; `future_tolerance_seconds` es 5 por defecto (0 explícito es tolerancia cero) y `poll_interval_seconds` es 15. Cada source define `provider_ref`, endpoint HTTPS normalizado, el mismo par, freshness, límites y `credential_ref` opcional. No se admiten credenciales, query ni fragment en la URL.

La respuesta cumple `contracts/rate-provider-v1.schema.json`; el precio son uint256 positivos numerator/denominator, nunca JSON number/float. Con un número par de fuentes se selecciona conservadoramente la mediana observada inferior, sin promedio ni redondeo. Todas las comprobaciones deben ser válidas; en caso contrario no se crea tick.

Se bloquean redirects, proxies, IP privadas/loopback/link-local/reservadas y DNS mixto; el DNS queda fijado por conexión. TLS, timeout, content type y tamaño están acotados. Los secrets solo se leen desde `RATE_SECRET_DIR` de solo lectura y no aparecen en snapshots, BD, logs ni health.

El bootstrap standalone habilita de inmediato `RUB`, `USD`, `EUR`, `KZT`, `INR` y `CNY` para `eth-ethereum`, `sol-solana`, `ton-ton`, `trx-tron` y `usdt-tron`: 30 policy targets. El gateway normalizado agrupa y almacena en caché las consultas. CoinGecko y CoinPaprika aportan las dos observaciones cripto; KZT usa el USD/KZT oficial diario del Banco Nacional de Kazajistán porque ambos proveedores no cotizan KZT directamente. `rate_gateway_origin` debe ser el origen HTTPS público durante el bootstrap standalone.

Env obligatorias: `DATABASE_URL`, UUID `RATE_WORKER_ID`, `RATE_TARGETS_JSON` global estricto. Se rechazan targets tenant; `base_asset` debe ser un `assets.id` activo y `quote_asset` un fiat de tres caracteres. Opcionales: `RATE_SECRET_DIR`, `RATE_POLL_INTERVAL=5s`, `RATE_LEASE_DURATION=30s`, `RATE_MAX_ATTEMPTS=8`, `RATE_MAX_READY_AGE=2m`, `RATE_HEALTH_ADDRESS=:9092`. Cree el rol NOLOGIN `rate_runtime_worker` antes de 000007 (o aplique grants después), haga que el login lo herede y cree la identity enabled.

`/healthz` comprueba BD; `/readyz` exige ticks recientes no vencidos para todos los targets y cero dead-letter. El commit guarda provenance inmutable y proyecta el mismo tick en `asset_rate_ticks`, leído por `PersistedPlanner`; unique pair y policy binding evitan ambigüedad. Un stale/divergent nunca es seleccionable.
