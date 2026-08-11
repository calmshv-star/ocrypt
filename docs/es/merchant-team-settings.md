# Equipo del comercio y ajustes del proyecto

Este módulo está aislado por tenant y comercio. Solo administra acceso humano y preferencias no financieras; tarifas, activos, redes, finality, matching, liquidación y tesorería continúan en los planos de control financiero y de plataforma.

El navegador nunca llama al servicio privado ni envía actor, tenant, comercio, permisos o aprobador. El BFF valida la sesión OIDC, correo verificado, issuer/subject, membresía y MFA, y firma una aserción `MerchantSettingsAdmin` de un solo uso y un minuto ligada al método, path/query canónico y SHA-256 del cuerpo. El enlace BFF–API usa mTLS y el API vuelve a leer los permisos en PostgreSQL.

## Roles y aprobación

`owner` tiene control total; `security_admin` controla la seguridad del equipo; `admin` gestiona equipo ordinario y ajustes; `developer` lee el equipo y modifica ajustes; `support` y `viewer` son de lectura. Los roles son cerrados: un cliente nunca define permisos. Conceder o retirar `owner`/`security_admin`, o deshabilitar/eliminar a quien los posee, exige una solicitud durable y otro actor MFA activo en otra sesión. Se rechazan autoaprobación y cambio de rol propio. La transacción vuelve a validar hash, versión, identidades, permisos, sesiones, MFA y caducidad. La base impide perder al último owner incluso con concurrencia.

## Invitaciones

Solo `admin`, `developer`, `support` y `viewer` pueden invitarse directamente. La aceptación es única, antes de caducar y con identidad OIDC cuyo correo verificado normalizado coincida. El miembro queda ligado a admin user, issuer, subject y correo; el otorgante registrado es quien invitó.

Un POST de mismo origen exclusivo para invitaciones decodifica el token canónico de 43 caracteres, calcula SHA-256 y resuelve la invitación antes de OIDC. El token original nunca se guarda ni registra, ni entra en state, return path, cookies o una URL enviada al BFF. El navegador elimina de inmediato el fragmento de entrega y conserva el token solo en `sessionStorage`. Son obligatorios state, nonce, PKCE, MFA, issuer/subject exactos, `email_verified` y correo invitado coincidente. Una identidad nueva se crea como `invited`, auditada y sin grants de plataforma; su sesión solo puede aceptar esa invitación. Membresía, consumo, activación y promoción de la sesión se confirman en una transacción PostgreSQL. Los registros vencidos quedan inertes y sus sesiones se revocan. Solo la misma sesión puede repetir la misma clave idempotente tras perder una respuesta.

`copy_once` se activa atómicamente y muestra el token de 43 caracteres solo en la primera respuesta; un replay nunca lo repite. `email` crea un trabajo durable. Solo se admite con heartbeat reciente y todos los key ID requeridos. El worker usa invitation ID como clave idempotente del proveedor, aplica lease y reintentos limitados, y activa únicamente tras ACK durable. Expiry y dead letter se auditan.

PostgreSQL conserva solo SHA-256 y un key ID no secreto. El token es `HMAC-SHA256(delivery_key, "merchant-invite-v1\0" || tenant_id || merchant_id || invitation_id)`. La clave es exclusiva. Para rotar: añadir la nueva, hacerla current, esperar todos los trabajos del key ID antiguo y después retirarla; el arranque bloquea una retirada prematura. Una filtración afecta únicamente invitaciones conocidas derivadas con esa clave hasta expiry/revoke, no credenciales API ni secretos financieros.

Deshabilitar o eliminar genera una señal durable; un worker mínimo revoca las sesiones admin y confirma la señal atómicamente. Los ajustes versionados incluyen nombre, locale `en/zh-CN/es/fr/de/ru`, zona IANA, correo de soporte opcional, preferencias de notificación y hasta 100 orígenes HTTPS exactos. Se prohíben HTTP, wildcard, credenciales, path, query y fragment. JSON duplicado/desconocido, secretos y políticas financieras se rechazan. Cada cambio exige version y motivo, crea snapshot inmutable y audit chain SHA-256.

API privada TLS 1.3 `:8447`, health `:9095`; revocación `:9096`; entrega `:9097`. Email es fail-closed. No registrar assertions, tokens, bearer, key ring, OIDC tokens ni respuestas completas del proveedor. Contrato: `contracts/merchant-settings-openapi.yaml`; toda mutación exige `Idempotency-Key`.
