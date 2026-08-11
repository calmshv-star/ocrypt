# Copia y restauración de PostgreSQL

## Objetivo y responsables

Proteger intents, observaciones de cadena, idempotencia, matching, ledger, outbox y
cursores. El dueño de la base opera las copias; el responsable del incidente aprueba
la restauración; riesgo/finanzas firma la conciliación; seguridad controla las
credenciales. Objetivos: RPO máximo de 5 minutos y RTO de 4 horas, salvo SLA más estricto.

Son obligatorios WAL/PITR continuo cifrado, export diario lógico, copia cifrada e
inmutable en otra cuenta o dominio de fallo, vigilancia de retención/frescura/gaps/
checksums, identidades distintas para runtime/migration/backup/restore, simulacro
aislado trimestral y ejercicio regional anual. «Backup enabled» no es evidencia:
guarde tiempos, continuidad WAL, checksums, recuperación de claves, duración,
conciliación y aprobación doble.

## Decisión previa

1. Declare incidente, detenga releases/migraciones y determine si la fuente está
   caída, corrupta, comprometida o lenta. Si hay riesgo de integridad, fence las
   escrituras y registre el último instante UTC fiable y su justificación.
2. Preserve logs, timelines, WAL, snapshots, evidencia de cadena y digests. Use PITR
   para borrado/error operativo y restauración lógica para validación independiente.
3. Nunca restaure sobre la base actual. Cree un destino nuevo, sin camino de red de
   aplicaciones. Un segundo revisor aprueba destino, punto, backup, claves y pérdida esperada.

## Restaurar y validar

1. Restaure con herramientas del proveedor a una instancia/cuenta nueva, con red
   privada, TLS, cifrado, auditoría y credencial temporal. Bloquee aplicaciones y callbacks.
2. Para archivo lógico use herramientas PostgreSQL compatibles, deténgase ante el
   primer error, conserve versión/salida y no restaure owner/ACL que amplíen permisos.
   Aplique solo migraciones forward compatibles; nunca una down migration.
3. Compruebe integridad, constraints/FK, extensions/collations, RLS enabled/forced y grants mínimos.
4. Concilie como mínimo:
   - cantidades/estados de intents y routes por tenant y tiempo;
   - unicidad de transfer identity y active payment matches;
   - suma cero de entries en cada ledger transaction;
   - idempotency keys y response hashes;
   - unmatched/manual resolutions y aprobadores;
   - identidad, orden, intentos y estado terminal de callback/outbox;
   - continuidad de scanner cursor/gaps frente a finalized chain independiente;
   - assignments/reservations sin leases activos en conflicto.
5. Compare el intervalo perdido con evidencia inmutable y prepare una lista aprobada
   de replay/compensación. Nunca acredite en masa solo por importe, hora, captura o IA.

## Conmutación

1. Cree credenciales runtime nuevas y mínimas, actualice Secrets externos y pruebe
   desde pods canary aislados. No reutilice credenciales de restore.
2. Inicie API con ingress cerrado; luego settlement, callback y scanner por separado.
   Scanner sigue desactivado sin release gate. Pruebe API firmada/idempotente, cola y callback.
3. Abra tráfico gradualmente observando readiness, errores DB, edad, duplicados,
   quorum y callbacks. Mantenga la base anterior fenced/read-only durante rollback.
4. Ejecute solo replay o compensaciones revisados, con external IDs estables y doble
   control. Informe al merchant del intervalo exacto y remedio.
5. Cierre tras medir RPO/RTO, firmar conciliación, validar backup del nuevo primary,
   probar alarmas, revocar credenciales antiguas y conservar evidencia.

El simulacro falla si faltan claves, existe un gap WAL, el destino no está aislado,
el release fijado no arranca, el ledger no balancea, cambian constraints de unicidad/
idempotencia, no se demuestra continuidad del scanner, se incumplen objetivos sin
riesgo aceptado o faltan dos aprobadores independientes.
