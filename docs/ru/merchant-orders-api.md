# Упрощённое API платёжных заказов

## Production

- API: `https://api.pay.example.com`
- Публичная оплата и загрузка чека: `https://pay.example.com`
- Key ID: `mk_live_example_v1`
- Секрет хранится на платёжном сервере в
  `/opt/ocrypt/secrets/core-api-secret`. Его нужно передать на backend магазина
  через secret-файл; в браузер, URL, логи и переменные frontend он попадать не
  должен.

## 1. Создание заказа

```http
POST https://api.pay.example.com/v1/merchant/orders
Content-Type: application/json
Accept: application/json
Idempotency-Key: order-123456
Merchant-Key-Id: mk_live_example_v1
Merchant-Timestamp: 1786469000
Merchant-Nonce: <новые 16–128 символов>
Content-Digest: sha-256=:<base64 SHA-256 точного body>:
Merchant-Signature: <base64url HMAC-SHA256 без padding>

{"order_id":"123456","customer_id":"customer-42","amount":"499.00","currency":"RUB","network":"tron","asset":"USDT","description":"Заказ #123456","expires_in":1800}
```

`amount` — точная десятичная строка в указанной валюте. Для RUB scale по
умолчанию равен 2. Один логический заказ всегда повторяется с тем же
`Idempotency-Key` и теми же байтами body.

Ответ `201`:

```json
{
  "data": {
    "payment_id": "019ff1c9-007b-7f97-b0f1-105d11b3133c",
    "order_id": "123456",
    "customer_id": "customer-42",
    "status": "pending",
    "amount": "499.00",
    "currency": "RUB",
    "payment": {
      "route_id": "019ff1c9-0095-7fc5-9c95-6963dd0d29c3",
      "network": "tron:mainnet",
      "asset": "usdt-tron",
      "address": "T...",
      "amount": "6.061234",
      "expected_amount_atomic": "6061234",
      "required_finality": 1,
      "top_up_allowed": false
    },
    "checkout_url": "https://pay.example.com/checkout?token=cs_...",
    "receipt_url": "https://pay.example.com/v1/checkout-sessions/cs_.../receipt",
    "expires_at": "2026-08-11T18:30:00Z",
    "updated_at": "2026-08-11T18:00:00Z",
    "version": 2
  },
  "request_id": "...",
  "api_version": "2026-08-01"
}
```

Backend магазина сохраняет `payment_id`, `checkout_url` и `receipt_url`. Последние два
являются bearer-capabilities и возвращаются только при создании или точном
идемпотентном повторе.

## 2. Проверка статуса

```http
GET https://api.pay.example.com/v1/merchant/orders/{payment_id}
Merchant-Key-Id: mk_live_example_v1
Merchant-Timestamp: ...
Merchant-Nonce: ...
Content-Digest: sha-256=:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=:
Merchant-Signature: ...
```

Для GET подписывается пустой body. Backend может опрашивать endpoint с разумным
backoff, но окончательную выдачу услуги лучше делать по webhook
`payment.settled`.

| `status` | Действие интеграции |
|---|---|
| `pending`, `observed`, `confirmed` | Показывать ожидание, ничего не выдавать |
| `partially_paid` | Показать `payment.remaining_amount` и те же реквизиты, если `top_up_allowed=true` |
| `settled` | Идемпотентно зачислить заказ. Упрощённый API также возвращает этот статус после подтверждённой переплаты; её размер остаётся в `payment.excess_amount` |
| `needs_review`, `reorg_review` | Показать ручную проверку |
| `expired`, `cancelled`, `reversed` | Не выдавать заказ |

При единственном подходящем заказе на адресе перевод сопоставляется по сети,
активу, адресу, времени и близости суммы без чека. Недоплата автоматически
принимается только в пределах процента, утверждённого в политике магазина.
Если сумма ниже этого предела, API возвращает `partially_paid` и точный остаток.
При нескольких кандидатах автоматическое зачисление запрещено.

Внутренний payment intent и журнал событий сохраняют статус и событие
`overpaid` для сверки. Это не блокирует выдачу услуги после того, как Ocrypt
однозначно сопоставил и финализировал перевод.

## 3. Загрузка чека

Интерфейс магазина показывает кнопку «Платёж не зачислился» и отправляет исходный JPEG, PNG
или WebP напрямую на `receipt_url`, полученный при создании заказа:

```http
POST {receipt_url}
Content-Type: image/jpeg
Idempotency-Key: receipt-{order_id}-{sha256_чека}

<raw bytes изображения>
```

Максимальный размер — 5 MiB. Ответ `202` имеет статус
`proof_queued` или `transaction_not_visible`. Анализ изображения не зачисляет
деньги сам: он только находит перевод, после чего обычный независимый
blockchain-verifier подтверждает его.

## 4. Подпись запросов

Сначала вычисляются SHA-256 точных байтов body и каноническая строка:

```text
METHOD + "\n" +
PATH_WITH_SORTED_QUERY + "\n" +
TIMESTAMP + "\n" +
NONCE + "\n" +
LOWERCASE_HEX_SHA256(EXACT_BODY)
```

`Merchant-Signature` — unpadded base64url от
`HMAC-SHA256(secret, canonical_string)`. `Content-Digest` — standard base64
того же SHA-256 в формате `sha-256=:...:`. Нельзя сериализовать JSON повторно
между вычислением подписи и отправкой.

## 5. Минимальная интеграция

1. На backend добавить клиент двух endpoints выше и HMAC-подпись.
2. При создании локального заказа вызвать `POST /v1/merchant/orders`.
3. Пользователю показывать `checkout_url` либо поля из `payment`.
4. Обрабатывать `partially_paid` и показывать точный `remaining_amount`.
5. Добавить загрузку чека на сохранённый `receipt_url`.
6. Зачислять услугу только один раз по `payment.settled`; GET использовать для
   восстановления состояния.
