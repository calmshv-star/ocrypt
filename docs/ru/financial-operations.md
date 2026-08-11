# Финансовые операции

Подсистема реализует изолированные по арендаторам казначейские sweep-переводы, проверенные возвраты и детерминированную сверку. Это внутренний операторский API, а не merchant endpoint. Все суммы — канонические строки uint256 в минимальных единицах; числа с плавающей точкой запрещены.

## Модель безопасности

- Каждая запись выполняется в PostgreSQL `SERIALIZABLE` с принудительным RLS арендатора.
- Idempotency key блокируется и связан с SHA-256 отпечатком запроса; другой payload даёт конфликт.
- Агрегат, резервы, сбалансированные проводки, hash-chain аудит и outbox фиксируются атомарно.
- Источник/nonce, дневной лимит и доступная сумма settlement блокируются до принятия операции.
- Для возврата нужно независимое доказательство кошелька, кастодиана или мерчанта. Наблюдаемый отправитель сам по себе не является проверкой; безопасное значение — возврат только на origin.
- Подтверждение требует step-up и второго оператора, отличного от создателя.
- Операторский API может запрашивать, читать, подтверждать, отменять и запускать только сверку. Маршрутов build/sign/broadcast в нём нет.

Finalized перевод — только evidence, но не проверка владения. Refund capability остаётся fail-closed, пока отдельно допущенный wallet-signature/custodian verifier не создаст неотозванную запись `financial_verified_refund_destinations`; endpoint для произвольного merchant evidence намеренно отсутствует. Origin-only всё равно требует независимой проверки и никогда автоматически не доверяет отправителю CEX, smart-contract, GasFree или hot wallet.

## Изоляция исполнения

`financial-worker` выполняет по одному этапу под fencing token. Builder, signer, broadcaster, независимый finality verifier и event sink обязаны иметь пять разных HTTPS origins и credentials. Redirect и proxy из окружения отключены. Каждый side effect получает стабильный idempotency key и привязку к агрегату. Signer получает только утверждённый digest и opaque reference; приватные ключи блокчейна платформа не хранит.

Finality приходит от отдельно допущенного verifier, а не от signer/broadcaster. Он выдаёт confirmed, finalized, failed или reorged с доказательством. Для возвратов finality/reorg создают неизменяемые балансирующие/реверсные проводки. Внутренний sweep учитывает статус и комиссию, но не изображает изменение собственности мерчанта.

## API, эксплуатация и допуск

Контракт — `contracts/financial-openapi.yaml`. IAM proxy подписывает tenant, actor, отсортированные permissions, ограниченный step-up, timestamp, nonce, path/query и digest тела. Nonce хранится в отдельной `financial_proxy_nonces`, без зависимости от merchant API clients. Чтение требует `financial:read`.

API требует `FINANCIAL_DATABASE_URL`, TLS certificate/key и `FINANCIAL_OPERATOR_ASSERTION_SECRET_FILE`; custody ports в процессе API принудительно отключены. Worker требует явный список tenant UUID и отдельные `FINANCIAL_{BUILDER,SIGNER,BROADCASTER,FINALITY,EVENT_SINK}_{URL,TOKEN_FILE}`. Для Kubernetes допустим health bind `:9093`, но публичного Service быть не должно.

Outbox использует `SKIP LOCKED`, монотонный lease token, retry, dead-letter после 20 попыток, стабильный event ID и обязательный echo ack. Аудит связан SHA-256 цепочкой на арендатора и добавляется только через `append_financial_audit`; последний hash нужно регулярно якорить вне PostgreSQL.

Перед включением денег: выполнить 000001 → опциональный независимый 000002 → 000003 up/down/up на отдельной БД; выдать минимальные права; проверить RLS двумя tenant; провести key ceremony для KMS/HSM/MPC, допустить независимые provider/sink, проверить потерянные ответы, stale fence, reorg, dead-letter, backup/restore и audit chain. Локальные тесты не требуют сокетов; live PostgreSQL проверка возможна только на явно выделенной тестовой БД.

## Финансовый кабинет администратора

Браузер обращается только к Admin BFF того же origin. BFF получает из актуальных ролей БД закрытый набор прав на весь tenant и отклоняет merchant-scoped роли и любые права из браузера. Казначейский оператор может создавать и отменять sweep и возвраты, а также запрашивать сверку. Независимый senior approver может согласовывать операции и выполнять сверку. Поддержка и платёжные операторы финансовых прав не получают.

Для каждой мутации обязательны CSRF, точный Origin, текущая версия, причина и `Idempotency-Key`. Повтор решения сохраняется атомарно с агрегатом, аудитом и outbox; та же ключевая строка с другим методом, path или телом даёт конфликт. Согласование требует недавней MFA и другого оператора. Атомарные суммы остаются строками в UI и API.

BFF подключается к приватному financial API только по TLS 1.3 с закреплённым CA, явным server name и проверяемым клиентским сертификатом. Мониторинг использует отдельный сертификат с минимальными правами; health endpoints не переводятся на открытый HTTP. Redirect и proxy окружения отключены. Браузер не получает внутренний origin, assertion secret и custody-данные; маршруты build/sign/broadcast/исполнения денег не публикуются. Live custody остаётся выключенным и fail-closed; наличие кабинета не означает, что signer или provider допущен в production.
