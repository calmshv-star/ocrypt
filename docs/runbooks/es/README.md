# Guías de operación

Estos procedimientos se aplican a producción y preproducción. Ante un incidente,
ejecute primero los controles comunes de la guía de incidentes críticos y después
el escenario correspondiente. Nadie debe recibir acceso de restauración de la base
de producción sin haber realizado un simulacro.

- [Incidentes críticos](critical-incidents.md): indisponibilidad, divergencia del
  escáner, reorganización, callbacks, pagos sin identificar y exposición de claves.
- [Copias y restauración](backup-restore.md): controles, restauración aislada,
  conciliación y conmutación.
- El procedimiento canónico de despliegue y rollback está en la sección inglesa.

El escáner está desactivado: su binario y adaptadores aún no superan la puerta de
release. Un Deployment o un health check correcto no autorizan la ingestión. Antes
de producción se exigen digests firmados, Secrets externos, roles de base separados,
NetworkPolicy efectiva, TLS a PostgreSQL, PITR y simulacro verificados, guardia y
alarmas probadas. Los health checks actuales no sustituyen métricas financieras y
de colas, que todavía no están implementadas.
