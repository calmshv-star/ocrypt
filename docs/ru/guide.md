# Универсальная криптоплатёжная платформа

Это краткое продуктовое, разработческое и эксплуатационное описание
самостоятельной универсальной криптоплатёжной платформы.

## Для владельца продукта

### Что делает платформа

Платформа подключается к сайтам, приложениям, ботам и SaaS как multi-tenant
платёжная инфраструктура. Клиентский backend создаёт payment intent; платформа
фиксирует котировку, выдаёт реквизиты, наблюдает сеть, сохраняет неизменяемые
chain events, сопоставляет их, проводит settlement в ledger и отправляет
подписанное событие.

За товар, подписку, баланс, бронь и customer identity отвечает подключённый
проект. Merchant сообщает достоверный статус оплаты, но не выдаёт чужой продукт.

### Полный функциональный объём

- Server API, hosted и embedded checkout, payment links.
- Нативные монеты и токены EVM, TRON, Solana, TON и одобренных Move-сетей.
- Точные, частичные, недоплаченные, переплаченные, поздние, fee-deducted,
  wrong-asset и internal smart-contract переводы.
- Durable cursors, несколько RPC, finality policies и reorg workflow.
- Финансовый ledger, callback outbox, история событий и reconciliation.
- Кабинет мерчанта, операторская очередь, platform admin и детерминированный
  sandbox.
- Опциональные refunds/sweeps через изолированный signer. Watch-only установка
  честно сообщает, что custody-функции недоступны.

### Обязательные свойства

- Один on-chain event нельзя зачислить дважды.
- В финансовой логике нет binary float.
- Поздность определяется временем блока, а не обнаружения сканером.
- AI только ранжирует детерминированных кандидатов и ничего не зачисляет.
- Ручное решение хранит автора, причину, версию объекта, evidence и отдельные
  подтверждения рисков.
- Reorg создаёт компенсирующий процесс, а не удаляет историю оплаты.
- Private keys отсутствуют в API, checkout, scanner и admin.

### Пользовательский путь

1. Backend проекта создаёт intent с order reference и суммой в minor units.
2. Route фиксирует актив, сеть, raw amount, адрес/memo, происхождение quote и
   expiration.
3. Checkout показывает сеть и contract/mint, точную net-сумму, QR, countdown,
   копирование и предупреждение о wrong network.
4. Система обнаруживает перевод и набирает finality. На этом этапе товар ещё не
   выдаётся.
5. Ledger commit создаёт `payment.settled`.
6. Webhook inbox клиента применяет событие ровно один раз и создаёт собственный
   fulfillment outbox.

Неоднозначный, поздний и wrong-asset перевод не исчезает, а попадает на review.
Отмена intent закрывает штатную оплату, но не прекращает наблюдение адреса.

## Для разработчика

### Главные объекты

- **Payment intent** — неизменяемое коммерческое требование.
- **Route** — версионированная котировка и реквизиты одного asset/network.
- **Transfer event** — нормализованный chain fact с transaction/event index.
- **Match/contribution** — аудируемое распределение перевода на route.
- **Settlement** — неизменяемый зачисленный результат в double-entry ledger.
- **Domain event** — версионированное внешнее событие с tenant sequence.
- **Delivery** — повторяемая транспортная попытка одного canonical body.
- **Unmatched case** — workflow проверки, а не редактируемая транзакция.

### Состояния платежа

```text
created → awaiting_route_selection → pending → observed → confirmed → settled
                                      │          └→ partially_paid
                                      ├→ expired → needs_review
                                      └→ cancelled
observed/partially_paid → needs_review → settled | reversed
settled → overpaid
confirmed/settled/overpaid → reorg_review → settled | reversed
```

Из `settled` нельзя вернуться в `pending`. Перевод идёт по
`observed → confirmed → finalized`; для реорганизации цепи и признания события
невалидным существуют отдельные `reorged` и `invalidated`.

Неопознанный платёж проходит `new → candidates_ready → bound →
verification_requested → verified → resolved`; отдельно существуют
`approval_required`, `verification_retry`, `conflict` и `reorged`. Принятие
недоплаты или другого актива требует второго сотрудника. Верификатор заново
читает сохранённое доказательство и блокчейн, а оператор не вводит сумму
зачисления вручную.

### Привязка перевода и доплата

Каждый route принадлежит ровно одному payment intent. Сканер привязывает
перевод по неизменяемым реквизитам route, а не по присланному клиентом tx hash.
Несколько переводов в разрешённом окне складываются атомарно только внутри
этого route; повторное событие не увеличивает итог второй раз.

Публичный checkout возвращает `amount`, `received_amount`,
`remaining_amount`, `payment_count` и `top_up_allowed`. При `partially_paid`
плательщик видит точный чистый остаток и доплачивает его на тот же адрес в той
же сети. Комиссию вывода нужно добавить сверху: на адрес должен поступить
именно `remaining_amount`. После закрытия окна `top_up_allowed=false`, QR и
кнопки копирования скрываются, а случай уходит в review. Новый invoice для
того же перевода не создаётся; повтор API-запроса с тем же idempotency key
возвращает исходный intent/session.

### Минимальный API-flow

```http
POST /v1/payment-intents
Idempotency-Key: order-2026-00042
Content-Type: application/json

{
  "merchant_order_id": "order-2026-00042",
  "amount_minor": "49900",
  "currency": "RUB",
  "currency_scale": 2,
  "description": "Годовой тариф",
  "customer_reference": "opaque-customer-17",
  "expires_in": 900,
  "allowed_routes": [{"provider": "on_chain", "chain_id": "tron:mainnet", "asset_id": "usdt-tron"}]
}
```

Суммы в JSON — строки с явным scale/decimals. Все mutation endpoints требуют
idempotency key. Повтор идентичного запроса возвращает прежний объект, а другой
immutable body под тем же ключом — `idempotency_conflict`.

Основные endpoints:

- `POST/GET /v1/payment-intents`;
- `POST /v1/payment-intents/{id}/routes`;
- `POST /v1/payment-intents/{id}/cancel`;
- `POST /v1/payment-proofs` только как подсказка для поиска tx;
- `GET /v1/events?after_sequence=...` для восстановления;
- transfers, balances и reconciliation reports;
- sandbox simulation для partial, wrong-asset, duplicate и reorg.

### Аутентификация API

HMAC вычисляется по исходным байтам body:

```text
HMAC-SHA256(secret,
  METHOD + "\n" +
  CANONICAL_PATH_AND_QUERY + "\n" +
  TIMESTAMP + "\n" +
  NONCE + "\n" +
  SHA256_HEX(RAW_BODY))
```

Передаются key ID, timestamp, 128-bit nonce, `Content-Digest` и signature.
Для повышенного уровня поддерживаются Ed25519 и mTLS. Sandbox/live credentials
разделены и имеют минимальные scopes.

### Как принимать webhook

Обычная выдача продукта происходит только по `payment.settled`:

1. ограничить размер и прочитать raw body;
2. до доверенного JSON проверить key ID, timestamp, nonce/delivery ID, digest и
   signature;
3. сверить merchant, environment, event type, order reference, amount/currency;
4. открыть DB transaction;
5. вставить уникальный `(event_id, body_digest)` во входящий inbox;
6. идентичный duplicate вернуть с прежним acknowledgment;
7. тот же event ID с другим digest отклонить как security conflict;
8. изменить локальный заказ и создать fulfillment outbox в той же транзакции;
9. commit и вернуть `acknowledged_event_id`.

Retry сохраняет event ID и canonical body, но получает новый delivery ID,
timestamp, nonce и signature. HTTP-порядок не гарантируется; для восстановления
используются tenant sequence и Events API.

Исполняемые примеры находятся в [`../../examples`](../../examples/README.md).

## Для эксплуатации

### Развёртывание

- Stateless API и admin BFF за ingress/WAF.
- PostgreSQL — единственный финансовый source of truth.
- Transactional outbox и leased workers; NATS/Redis не заменяют ledger.
- Отдельные indexer-процессы по сетям с durable cursor и RPC fallback/quorum.
- Независимые delivery, rate, expiry и reconciliation workers.
- Treasury signer изолирован от Internet и требует approval policy.

### Метрики и трассировка

Intent, route, transfer, match, settlement, event и delivery несут общий trace.
Нужно отслеживать:

- scanner lag и расхождение RPC;
- observed→finalized→settled latency;
- возраст и размер unmatched queue;
- callback backlog, retries и dead letters;
- idempotency conflicts и signature/replay failures;
- ledger/reconciliation diff;
- возраст quote, сбои rate providers и ёмкость address pool;
- reorg, manual overrides, refunds и операции signer.

### Инциденты

По возможности ставится на паузу только проблемный asset/route. Runbooks нужны
для RPC outage, scanner lag, reorg, callback outage, роста unmatched, ошибки
курса, компрометации ключа, ledger mismatch, signer failure и восстановления БД.
Финансовую историю нельзя исправлять ручным UPDATE: используются новые версии
конфигурации и компенсирующие события.

### Резервирование и retention

Требуются зашифрованный continuous WAL, проверяемый PITR, object storage для
evidence/reports и регулярное восстановление в изолированной среде. Audit
partitioned, redacted и удаляется/архивируется по policy; WORM применяется там,
где нужен неизменяемый архив.

### Допуск в production

Обязательны contract, concurrency, duplicate, exact-money, reorg, unmatched,
security, i18n, accessibility, load, soak, migration, restore и reconciliation
тесты. Полный независимый план: [`../TEST_PLAN.md`](../TEST_PLAN.md). Пропущенный
P0-инвариант блокирует релиз.
