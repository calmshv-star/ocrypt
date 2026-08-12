# Руководство по интеграции API

## Граница доверия и доступы

Создавайте отдельный HMAC-клиент для каждого сервиса и окружения. Платёжному backend обычно нужны `payments:write`, `payments:read` и `events:read`; для выгрузок используйте отдельный ключ с `reconciliation:read`. Добавляйте `payment-links:read`, `payment-links:write` и `checkout:write` только по необходимости. Ключ и секрет нельзя отдавать браузеру, пользователю бота, мобильному приложению, checkout, логам, URL или поддержке.

Merchant-запросы идут на API origin, а payment-link/checkout aliases — на management/gateway origin. Публичные `pl_…` и `cs_…` — высокоэнтропийные bearer-capabilities с ограничением по времени, действиям или числу применений.

## Валюты счёта и курсы

Валюта счёта не зашита в RUB. `currency` содержит ровно три заглавные ASCII-буквы и должна соответствовать коду ISO 4217; `currency_scale` явно задаёт число знаков после запятой. Для `RUB`, `USD`, `EUR`, `KZT`, `INR` и `CNY` обычно используется scale `2`: например, `amount_minor: "3813"` означает `38,13` выбранной валюты. API не угадывает scale по коду.

Сам по себе допустимый код валюты ещё не означает, что криптокотировка доступна. До создания on-chain или hosted route в production должен существовать свежий допущенный курс для точной пары `asset_id`/валюта. При отсутствующем, устаревшем, не набравшем quorum, будущем или чрезмерно разошедшемся курсе система закрывается с ошибкой, а не выдаёт приблизительную сумму. Для каждой валюты продаж нужно настроить и допустить независимые нормализованные источники курса.

## Жизненный цикл платежа

1. Создайте intent с уникальным `merchant_order_id`, точной строкой `amount_minor`, scale валюты, сроком и допустимыми маршрутами.
2. После выбора сети/актива создайте route и сохраните atomic amount, адрес/memo, срок котировки и `grace_ends_at`.
3. Показывайте только данные route из API. Чек кошелька и payment proof — лишь подсказка для поиска, а не подтверждение оплаты.
4. Проверяйте webhook и обрабатывайте его долговечно. Выдавайте продукт по `payment.settled`, идемпотентно по intent/order.
5. Восстанавливайте пропуски через `GET /v1/events?after_sequence=N`: доставка может дублироваться и идти не по порядку.

Cancel и expire прекращают обычное ожидание, но не стирают историю блокчейна. До конца grace-окна route остаётся сопоставимым, поэтому поздний перевод может перейти в review или settlement. Адрес нельзя немедленно переиспользовать. Metadata — только нефинансовые allowlisted поля; обновление требует `expected_version`, а после `409` нужно перечитать intent и принять решение заново.

## Подпись, повторы и webhooks

Подписывайте ровно сериализованные байты и канонический path/query. Nonce одноразовый. При транспортной ошибке, `429` и допустимых `5xx` применяйте exponential backoff с jitter, сохраняя тело и idempotency key. Validation/version conflict автоматически не повторяйте.

Webhook проверяется по исходным байтам до JSON parsing: digest, timestamp, event ID, key ID и HMAC над `<event-id>.<timestamp>.<raw-body>`. Во время ротации разрешайте старый и новый `key_id`. В одной транзакции заблокируйте `(event_id, body_digest)`, измените заказ, создайте fulfillment outbox и commit; только затем верните точный acknowledgement. Тот же event ID с другим digest — инцидент.

## Payment links, checkout и сверка

Payment link сейчас содержит ровно один route. `public_url` раскрывается только при создании или точном replay; list/get не возвращают bearer secret. Redeem атомарно расходует use, создаёт intent/quote/address/route и выдаёт `cs_…`. Return URL задаёт merchant, браузер его не переопределяет. Embedded token привязывайте к точному HTTPS Origin, а explorer URL стройте по собственной allowlist.

Для сверки используйте balances и operational summary. Аудиторский отчёт ограничен 366 днями и будущим временем. После статуса `ready` проверьте размер, SHA-256, зафиксированный `signing_key_id` и Ed25519 signature до разбора JSONL. Храните старые public keys весь retention period. Header фиксирует global ledger sequence/cutoff, footer содержит точные строковые totals.

## Sandbox и запуск

Проверьте exact/partial/over/late, wrong asset, duplicate delivery и settle-then-reorg, разрыв последовательности событий, ротацию webhook/report keys и восстановление после timeout. Sandbox не заменяет production admission: нужны реальные независимые providers, reorg/finality и restore drills, pinned images, rotation и load/soak evidence.

Team/settings — отдельная cabinet API, не часть merchant HMAC SDK. Backend-контракт уже существует, но BFF/browser activation пока pre-release; см. [настройки команды](merchant-team-settings.md) после передачи этого компонента.

Эталонные транзакционные адаптеры для FastAPI/Django, Laravel/Symfony, Express/NestJS, Spring Boot, ASP.NET, Telegram и generic commerce находятся в [индексе framework skeletons](../../examples/frameworks/README.md). Это шаблоны адаптации, а не устанавливаемые зависимости.
