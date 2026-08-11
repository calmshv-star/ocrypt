# Миграция JSON-MD5/Form-MD5

Legacy-адаптер — временный и по умолчанию выключенный мост. Он создаёт обычные intent/route в core, читает статус через PostgreSQL-backed API и отправляет старый callback только после канонического события `payment.settled`. Он не может пометить платёж оплаченным и не имеет доступа к ledger.

Перед запросом допуска примените миграцию `000018_legacy_compatibility`.

До допуска создайте HMAC-ключ core со scopes `payments:read`, `payments:write`, `events:read`, смонтируйте HMAC- и MD5-секреты как файлы, укажите HTTPS callback на порту 443 и однозначное соответствие currency/token/network маршруту core. Запрос и подтверждение 30-минутного допуска выполняют две разные операторские учётные записи.

Подпись строится из отсортированных непустых `key=value` плюс secret. JSON-MD5 исключает `signature`, Form-MD5 — `sign` и `sign_type`. `trade_id` содержит 128 бит секрета. Polling статуса только восстанавливает чтение. Начисляйте результат идемпотентно и отвечайте строго `ok` или `success` в нижнем регистре.

До даты заголовка `Sunset` перейдите на core HMAC API и канонические webhooks. Просроченный допуск, gap событий, потерянный secret или TLS-сбой закрывают readiness. Репозиторий не подтверждает live-проверку PostgreSQL/Kubernetes/callback.
