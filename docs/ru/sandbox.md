# Детерминированный sandbox для мерчанта

Sandbox — отдельный тестовый продукт, а не флаг live-платежей. Маршруты `/v1/sandbox/*` регистрируются только при `APP_ENV=sandbox|test`, `SANDBOX_RUNTIME=postgres`, отдельной БД и тестовом ключе с префиксом `mk_test_`. В production и обычном development возвращается `404`; PostgreSQL дополнительно отклоняет live-мерчантов.

Сначала вызовите `GET /v1/sandbox/workspace`: ответ содержит тестовые часы, версию, метаданные ключа с удалённым секретом, детерминированные адреса и HMAC-токен для reset. Создание через `POST /v1/sandbox/scenarios` формирует sandbox-only payment intent и route; эти UUID недоступны через `/v1/payment-intents`. Суммы передаются точными целыми строками.

Сценарии: `exact_payment`, `partial_payment`, `underpayment`, `overpayment`, `late_payment`, `wrong_asset`, `duplicate_callback`, `out_of_order_callback`, `timeout`, `dead_letter`, `reorg`, `reorg_recovery`. Пошаговый `POST /v1/sandbox/scenarios/{id}/actions` моделирует observation, confirmations, finality, callback, reorg и recovery. Settlement невозможен без требуемых подтверждений и отдельного finality. После reorg наблюдение включается заново, а подтверждения набираются повторно. `POST .../{id}/run` выполняет шаблон атомарно и идемпотентно.

`GET /v1/sandbox/callbacks` возвращает канонические байты JSON, SHA-256, попытки и статус с курсорной пагинацией. Секреты и тексты ошибок/ответов не сохраняются: остаются только категория и число байт. Reset требует idempotency key, актуальную версию workspace и HMAC-токен; удаляются только строки этого мерчанта в `sandbox_*`, без каскада в live-таблицы.

Успешный sandbox не заменяет production-проверки провайдеров, finality/reorg, восстановления, ротации ключей, закреплённых артефактов и нагрузки. AI не наблюдает, не подтверждает и не проводит settlement.
