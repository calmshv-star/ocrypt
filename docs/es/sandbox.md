# Sandbox determinista para comercios

El sandbox es un producto de prueba separado, no un interruptor del motor live. `/v1/sandbox/*` solo se registra con `APP_ENV=sandbox|test`, `SANDBOX_RUNTIME=postgres`, una base dedicada y una credencial `mk_test_`. Producción y desarrollo normal responden `404`; PostgreSQL también rechaza comercios live.

`GET /v1/sandbox/workspace` devuelve reloj, versión, credencial redactada, direcciones de prueba deterministas y el token HMAC de reset ligado a la versión. `POST /v1/sandbox/scenarios` crea un payment intent y una route exclusivos del sandbox: sus UUID no existen en `/v1/payment-intents`. Todos los importes son cadenas enteras exactas.

Escenarios: `exact_payment`, `partial_payment`, `underpayment`, `overpayment`, `late_payment`, `wrong_asset`, `duplicate_callback`, `out_of_order_callback`, `timeout`, `dead_letter`, `reorg` y `reorg_recovery`. `POST /v1/sandbox/scenarios/{id}/actions` simula observación, confirmaciones, finalidad, callbacks, reorg y recuperación paso a paso. No hay settlement sin las confirmaciones requeridas y una finalidad explícita. Tras un reorg, la observación se reincorpora y vuelve a confirmarse. `POST .../{id}/run` ejecuta la plantilla de forma atómica e idempotente.

`GET /v1/sandbox/callbacks` muestra el JSON canónico, SHA-256, intentos acotados y estado con paginación por cursor. No guarda ni expone secretos ni textos de error/respuesta. Reset exige clave de idempotencia, versión actual y token HMAC, y solo elimina filas `sandbox_*` del comercio.

Superar el sandbox no sustituye pruebas de proveedores reales, finalidad/reorg, restauración, rotación, artefactos fijados o carga. La IA no observa, confirma ni liquida pagos.
