# Администрирование конфигурации платформы

Platform Admin API — внутренний контур управления tenants, проектами и environments, сетями, версиями контрактов активов, watch-only пулами адресов, RPC capabilities, rate sources/policies, finality/matching policies, квотами, ссылками на notification channels, feature flags и maintenance windows. Private keys, seed-фразы, API secrets и credentials здесь не принимаются и не хранятся.

## Граница доверия

API работает только по внутреннему TLS. Браузер его не вызывает и не получает assertion key. Доверенным issuer выступает same-origin admin BFF: он проверяет серверную сессию, Origin/CSRF, MFA и DB role binding для конкретного tenant, после чего подписывает одноразовый assertion, связанный с method, escaped path, canonical query, точным hash body, audience, expiry и nonce. Grants из JSON браузера не принимаются.

Планируемые BFF routes: `/admin/v1/platform/changes`, `/admin/v1/platform/changes/{id}/{action}`, `/admin/v1/platform/snapshots`, `/admin/v1/platform/emergency-pauses`; они проксируют `/internal/platform-admin/v1/*`. Пакет предоставляет `platformadmin.AssertionIssuer`, но существующий BFF в этой изолированной задаче не изменён. React продолжает использовать только session и CSRF cookie.

Разрешения: `platform_config:read|write|request|approve|schedule|activate|rollback|emergency`. Auditor только читает; security admin создаёт/отправляет изменения и выполняет emergency actions; senior approver независимо утверждает, планирует, активирует и создаёт rollback draft. Tenant binding не расширяется на другой tenant.

## Версионный workflow

`draft → approval_requested → approved/rejected → scheduled → active`; прежняя версия определяется как superseded. Approval выполняет другой сотрудник с недавним MFA. Scheduled worker получает due rows через `SKIP LOCKED`, bounded lease и монотонный claim token. Activation атомарно добавляет immutable snapshot и activation record и сдвигает только fenced head pointer. Rollback копирует исторический snapshot в новую версию и снова проходит approval. Прямой edit production row, `PUT` и `PATCH` отсутствуют.

Emergency pause/resume — отдельный append-only поток с MFA, reason, idempotency, audit и outbox. Все мутации используют expected row version/head fence. Idempotency связана также с method/path/query/body, поэтому ключ нельзя переиспользовать для другого объекта. RLS/FORCE и composite FKs удерживают tenant/kind/logical key. Audit — единая hash-chain; definer function восстанавливает RLS-контекст вызывающего transaction.

Money-like значения и quota limit передаются точными unsigned decimal strings. Проверяются chain-specific contract addresses, rate quorum/staleness/spread, HTTPS endpoints без credentials, watch-only key references и границы maintenance windows. Inline secrets и несовпадающий payload hash отклоняются Go и SQL.

## Runtime-интеграция

Rate worker использует point-read контракт `ActiveSnapshotReader`. Scanner перед каждым циклом атомарно читает через `RuntimeStateReader` chain, finality, все RPC/asset и maintenance snapshots вместе с последним emergency pause. Отсутствующий, stale/unfenced, paused или несогласованный ресурс останавливает цикл. Confirmations вычитаются из quorum head, `overlap` не может быть меньше `reorg_depth`; snapshot/fence evidence каждой записанной высоты сохраняется в immutable `scanner_runtime_config_evidence`. RPC credential хранится только во внешнем файле, на который указывает `credential_ref`; production требует JSON вида `{"chain":"eip155:1","finality":"eip155:1","rpc_providers":["rpc/one","rpc/two"],"assets":["eth-ethereum","usdt-ethereum"],"maintenance":["eip155:1"]}` в `SCANNER_PLATFORM_RUNTIME_JSON`, статический режим разрешён только в development/test.

Planner проверяет admission в той же serializable-транзакции, где выбирает rate tick и арендует адрес. Соглашение ключей: tenant `merchant_environment` = UUID merchant, global chain/finality = chain ID, global asset = asset ID, tenant wallet pool = UUID wallet. Все snapshots должны быть active/current; tenant flag `new_routes` обязателен, enabled и с rollout 10000 bps. Отсутствующий/выключенный flag, emergency pause или активное окно `read_only`/`disable_new_routes` запрещают новый route. Quote сохраняет snapshot IDs/fences environment/chain/asset/finality и required finality, assignment — evidence wallet pool; decimals обязаны совпадать.

Matching не расширяется platform snapshot: он остаётся под отдельным four-eyes workflow. Quota, notification channel и finality для финансовых sweep/refund пока не являются runtime consumers; их activation нельзя считать применённой. Refund destination по-прежнему требует независимой проверки.

`platform-outbox-publisher` получает fenced lease только для enabled identity `outbox_publisher`, отправляет событие по TLS 1.3 + mTLS + bearer-file на точный `/v1/platform-admin/events` и отмечает publish только после ответа с совпадающими `event_id` и `claim_token`. Redirect/proxy запрещены, health listener только loopback. Live PostgreSQL/provider/destination проверки остаются обязательным deployment admission и package tests их не заменяют.
