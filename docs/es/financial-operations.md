# Operaciones financieras

Este subsistema ofrece sweeps de tesorería aislados por tenant, reembolsos verificados y conciliación determinista. Los importes son cadenas uint256 canónicas en unidades atómicas; no se admiten flotantes.

## Modelo de seguridad

Cada escritura usa PostgreSQL `SERIALIZABLE` con RLS forzado. La clave de idempotencia se bloquea y vincula a una huella SHA-256. Agregado, reservas, asiento doble equilibrado, auditoría encadenada y outbox se confirman atómicamente. Fuente/nonce, límite diario y saldo reembolsable se reservan bajo bloqueo. El remitente observado no prueba propiedad; el origen verificado es el valor seguro por defecto. Aprobar exige step-up y un segundo operador.

## Aislamiento de ejecución

Una transferencia finalizada solo prueba el pago, no la propiedad. Refund permanece fail-closed hasta que un verificador wallet-signature/custodian admitido por separado escriba `financial_verified_refund_destinations`; nunca se confía automáticamente en remitentes CEX, contratos o hot wallets.

La API de operador no tiene rutas build/sign/broadcast. `financial-worker` avanza una etapa por fencing token. Builder, signer, broadcaster, verificador de finalidad independiente y event sink usan cinco orígenes HTTPS y credenciales diferentes. Redirecciones y proxies del entorno están desactivados. Cada efecto externo lleva una clave de idempotencia estable y binding del agregado. Nunca se guardan claves privadas blockchain.

La finalidad procede del verificador quorum admitido aparte, no del signer/broadcaster. Finalidad y reorg de refunds generan asientos inmutables de balance/reversión. La outbox usa `SKIP LOCKED`, lease tokens monótonos, retry, dead-letter tras 20 intentos y ack con el mismo event ID.

## API y operación

Consulte `contracts/financial-openapi.yaml`. El proxy IAM firma tenant, actor, permissions ordenados, step-up acotado, timestamp, nonce, ruta/query y digest. `financial_proxy_nonces` no depende de clientes merchant. Leer requiere `financial:read`.

La API requiere base de datos, TLS y `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`. El worker requiere UUID tenant explícitos y pares separados `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}`. `:9093` sirve para probes del Pod de Kubernetes, sin Service público.

La auditoría se encadena por tenant con SHA-256 mediante `append_financial_audit`; ancle el último hash fuera de PostgreSQL. Antes de habilitar: migración up/down/up, mínimo privilegio, RLS con dos tenants, ceremonia KMS/HSM/MPC, admisión de providers y pruebas de respuesta perdida, stale fence, reorg, dead-letter, backup/restore y cadena de auditoría.

## Gabinete financiero de administración

El navegador solo se comunica con el Admin BFF del mismo origen. El BFF obtiene de los roles actuales de la base de datos un conjunto cerrado de permisos para todo el tenant; rechaza roles de ámbito merchant y permisos enviados por el navegador. El operador de tesorería puede solicitar o cancelar sweeps y reembolsos y pedir conciliaciones. Un aprobador senior distinto puede aprobarlos y ejecutar la conciliación. Soporte y operadores de pagos no reciben permisos financieros.

Cada mutación exige CSRF, Origin exacto, versión actual, motivo e `Idempotency-Key`. El replay de decisiones se guarda atómicamente con agregado, auditoría y outbox; reutilizar la clave con otro método, ruta o cuerpo genera conflicto. La aprobación requiere MFA reciente y un actor diferente. Los importes atómicos siguen siendo cadenas en UI y API.

El BFF usa TLS 1.3 hacia la API financiera privada, con CA fijada, nombre de servidor explícito y certificado de cliente obligatorio. La monitorización usa otro certificado de cliente con privilegios mínimos; los health endpoints no se degradan a texto plano. Se prohíben redirects y proxies del entorno. El navegador no recibe origen interno, secreto de assertion ni datos de custody, y no hay rutas build/sign/broadcast/execute de dinero. Custody real permanece deshabilitado y fail-closed; la interfaz no afirma que un proveedor haya sido admitido.
