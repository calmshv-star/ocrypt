# Procedimiento de migración en sombra

La migración `000021` mantiene PostgreSQL como autoridad contable. `migration-control` valida sin conexión y solo en dry-run. El inventario acotado sin secretos se firma sobre sus bytes canónicos exactos con dos claves Ed25519 autorizadas distintas y se envía por la API admin limitada al tenant.

Las transiciones pasan por inventario, validación, solicitud/aprobación/ejecución separadas, importación, sombra y canario. El cambio queda pendiente hasta que el actuador autenticado por separado confirme la versión y el fence exactos. Abortar el canario o revertir conserva hechos y vallas de propiedad; las direcciones watch-only importadas nunca se liberan.

El Job empieza con `MIGRATION_EXECUTE=false`; escribir exige rol dedicado, lease/fence, mTLS 1.3 y hechos firmados por quorum. La retirada exige backlog cero calculado por la base y pruebas inmutables de archivo, restauración y revocación de claves.

No se ejecutaron localmente la base origen, cadena, corte PostgreSQL, actuador, clúster Helm ni quorum de proveedores reales; añada esas pruebas al manifiesto de entrega.
