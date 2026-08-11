# Operaciones de proveedores

Es un plano de control y salud sin secretos. El gabinete muestra identidad estable,
operación, versiones de política/circuito, estado cerrado, retraso, tiempos e
identidades de aprobación; nunca endpoints, credenciales, firmantes, direcciones ni
errores sin filtrar.

El scanner de producción admite solo proveedores on-chain activos con evidencia
inmutable y vigente de `rpc_provider`, circuito cerrado y quorum fresco de dominios
de fallo independientes. Paused, open, stale o divergent se excluyen; sin quorum el
escaneo se detiene. Timeout, reintentos, backoff, rate limit, prioridad y límites de
salud vienen de la política aprobada.

Pausar y reactivar son acciones separadas de cuatro ojos con MFA reciente, versión,
motivo e idempotencia exactos; cambio, auditoría y outbox son atómicos. El worker
privado en 9100 usa leases cercados y probes de solo lectura por operación;
readiness exige un ciclo exitoso no vacío y un grupo de peers admisible. Hosted
nace pausado: callbacks firmados se conservan como evidencia append-only sin
ledger/settlement, y las salidas siguen bloqueadas hasta política aprobada y probe
correcto.

La solicitud vincula las seis políticas exactas y una referencia inicial limitada
y de solo escritura a un resumen inmutable. La referencia nunca vuelve en listas
ni decisiones. Tras la aprobación independiente con MFA, se requieren sondeos de
estado correctos desde al menos dos dominios de fallo y una reactivación separada
con cuatro ojos.
