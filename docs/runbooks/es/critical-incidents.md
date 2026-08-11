# Procedimientos para incidentes críticos

## Controles comunes

1. Declare severidad, inicio UTC, responsable del incidente, responsable técnico y
   relator; añada seguridad si procede. Use un único canal auditado.
2. Detenga releases, migraciones, rotación de claves, backfills y resoluciones
   manuales. Registre los últimos digests y hashes de configuración válidos.
3. Documente entorno, número de tenants, cadena/activo, intervalo y transición de
   estado. No publique secretos, cuerpos de callback, direcciones ni datos personales.
4. Aísle de forma reversible: retire tráfico del gateway, escale solo el worker
   afectado a cero o corte su egress. No borre filas, mueva cursores, fuerce «paid»
   ni reintente callbacks con SQL improvisado.
5. Preserve health/readiness, eventos de Deployment, logs depurados, edad de colas,
   heads de proveedores, salud de la base e historial de releases. Cambios de saldo
   o derechos requieren doble revisión y una lista de IDs inmutable y restringida.
6. Cierre solo tras recuperar backlog, comprobar invariantes, remediar clientes,
   probar alarmas, conservar evidencias y asignar las acciones correctivas.

## API o worker no disponible

1. Compare `/healthz` y `/readyz`: vivo pero no listo suele indicar base o credencial;
   ambos fallan suele indicar proceso, imagen, scheduling, memoria o nodo.
2. Revise réplicas deseadas/listas, reinicios, PDB, pods pendientes, presencia de
   claves Secret sin mostrar valores, pool, TLS, DNS y cambios de NetworkPolicy.
3. Si el release es la causa, restaure solo ese Deployment al digest registrado. No
   revierta el esquema. Si duda de la integridad de la base, pare escritores y restaure.
4. Antes de abrir gradualmente, pruebe readiness, petición firmada, creación
   idempotente de intent, avance de settlement y un callback controlado.

## Retraso del escáner o discrepancia de proveedores

Aplica únicamente cuando el escáner haya superado su puerta de producción.

1. Escale scanner y settlement a cero; mantenga callbacks ya confirmados. No avance
   el cursor ni reduzca finality para recuperar tiempo.
2. Registre identidad de cada RPC, chain/genesis, finalized head, hash del último
   bloque acordado, latencia y error. Una captura del explorer o recibo no basta.
3. Retire un proveedor solo si los restantes son realmente independientes y mantienen
   quorum. Localice la última altura cuya cadena hash/parent coincide por quorum.
4. Reescanee con solapamiento en almacenamiento comparativo aislado. Concilie
   identidad única, allowlist de contrato/mint, decimals, destinatario, logs/llamadas
   internas/instrucciones y umbral de confirmación.
5. Reanude primero un shard canary y luego settlement; vigile edad y duplicados. No
   acredite automáticamente unmatched ni candidatos sugeridos solo por IA.

## Reorganización de cadena

1. Pause scanner y settlement de esa cadena. Registre hashes anterior/nuevo,
   ancestro común, profundidad, quorum e IDs afectados.
2. Determine si el evento huérfano superó finality y produjo ledger o derechos;
   conserve ambas historias. No borre observations ni edite ledger entries.
3. Use el flujo diseñado de reorg/reversal. Una corrección financiera es una nueva
   transacción compensatoria, vinculada y balanceada, con doble control.
4. Reescanee desde antes del ancestro, concilie y solo entonces reanude. Informe al
   merchant con IDs estables y estado de remediación, sin prometer irreversibilidad.

## Fallo de callbacks

1. Confirme que settlement es correcto: el callback notifica, no es la fuente de
   verdad. Mida la entrega pendiente más antigua y endpoints afectados.
2. Revise readiness, leases, DNS/HTTPS egress, certificados, clases de respuesta y
   descifrado de envelope key sin exponer URLs con credenciales ni cuerpos.
3. Tras corregir, deje que la cola durable reintente los IDs y firmas originales.
   No genere eventos duplicados ni eluda SSRF. Limite por endpoint, respete backoff
   y confirme la deduplicación del receptor.

## Aumento de pagos sin identificar

1. Detenga matching automático y manual sobre el umbral aprobado, conservando la
   ingestión. La IA solo asesora y nunca autoriza un abono.
2. Segmente por chain, contrato/mint, destinatario, mecanismo, decimals, tiempo y
   versión de route; revise agotamiento de direcciones y caducidad de assignments.
3. Valide normalización con dos RPC independientes y fixtures de contratos/exchanges.
   Una captura no es proof.
4. Corrija parser/route hacia delante y repita en almacenamiento comparativo. Cada
   resolución manual exige doble control e idempotencia/unicidad demostradas.

## Exposición de credencial o clave

1. Considérela comprometida y no pida reenviarla. Revóquela en la autoridad y aísle
   el workload. Preserve auth logs, auditoría de secretos, flujos, digest e historial.
2. Emita un reemplazo de mínimo privilegio, actualice el Secret externo, reinicie
   solo el workload afectado y confirme que el valor anterior es rechazado.
3. No cambie sin más una envelope key: necesita descifrado versionado, recifrado
   verificado de todas las filas y clave de rollback bajo doble control. Si hay duda,
   cree primero un snapshot.
4. Revise intents, resoluciones, callbacks y ledger no autorizados; compense sin
   borrar historia y notifique por el proceso legal/de seguridad aprobado.

## Corrupción de base o split-brain

1. Retire escrituras, pare API/scanner/workers y fence el primary sospechoso. Nunca
   permita dos primarios escribibles ni reinicie almacenamiento repetidamente.
2. Preserve logs, WAL, timelines, snapshots, evidencia de cadena y último UTC fiable.
3. Restaure una base nueva y aislada, concilie y conmute de forma controlada. Nunca
   sobrescriba la única copia ni improvise reparaciones de filas.
