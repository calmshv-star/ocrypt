# Резервное копирование и восстановление PostgreSQL

## Цель и ответственность

Нужно сохранить intents, chain observations, idempotency, matching, ledger, outbox
и scanner cursors. Владелец БД отвечает за копии, руководитель инцидента разрешает
production restore, risk/finance подписывает сверку, security контролирует restore
credentials. Цель: RPO не более 5 минут и RTO 4 часа, если не утверждены более
строгие значения.

Обязательны непрерывный зашифрованный WAL/PITR, ежедневный логический export для
независимой проверки, immutable копия в другом account/failure domain, контроль
retention и свежести, раздельные runtime/migration/backup/restore роли, ежеквартальный
изолированный restore и ежегодная региональная тренировка. Галочка «backup enabled»
не доказательство: храните timestamps, WAL continuity, checksums, сведения о ключах,
время восстановления, результат сверки и подписи двух проверяющих.

## Решение о восстановлении

1. Объявите инцидент, остановите релизы/миграции и выясните: источник недоступен,
   повреждён, взломан или только медленный. При риске целостности закройте запись и
   fence primary. Зафиксируйте последний достоверный UTC-момент и основание.
2. Сохраните logs, timelines, WAL, snapshots, chain evidence и digest конфигурации.
   PITR применяйте при удалении/ошибке; logical restore — для независимой проверки.
3. Никогда не восстанавливайте поверх текущей БД. Создайте новый изолированный
   target без сетевого пути от приложений. Второй проверяющий утверждает target,
   restore point, backup set, keys и ожидаемое окно потери.

## Восстановление и сверка

1. Восстановите новую instance штатным provider tooling: private network, TLS,
   encryption, audit и временный restore credential. Запретите приложение и callback.
2. Для logical archive используйте совместимую версию PostgreSQL, прекращайте при
   первой ошибке, сохраняйте tool/version/output и не переносите owner/ACL, способные
   выдать лишние права. Применяйте только совместимые forward migration; down нельзя.
3. Проверьте целостность PostgreSQL, constraints/FK, extensions/collations, включённый
   и forced RLS и least-privilege grants.
4. Обязательно сверяйте:
   - counts и состояния intents/routes по tenant и времени;
   - уникальность transfer identity и активных payment matches;
   - нулевую сумму entries каждой ledger transaction;
   - idempotency keys и response hashes;
   - unmatched/manual resolutions и approvers;
   - callback/outbox identity, порядок, attempts и terminal state;
   - непрерывность scanner cursor/gaps с независимым finalized chain;
   - отсутствие конфликтующих address assignments и amount reservations.
5. Сопоставьте потерянный RPO-интервал с immutable chain/provider evidence и создайте
   утверждённый replay/compensation list. Нельзя массово зачислять только по сумме,
   времени, скриншоту или AI score.

## Переключение

1. Создайте новые least-privilege runtime credentials, обновите внешние Secret и
   проверьте соединение из изолированных canary pods. Restore credential не используйте.
2. Запустите API при закрытом ingress, затем settlement, callback и scanner отдельно.
   Scanner остаётся выключенным без release gate. Проверьте signed/idempotent API,
   контролируемую очередь и callback.
3. Открывайте трафик постепенно, следя за readiness, DB errors, queue age, дублями,
   quorum и callback failures. Старую БД оставьте fenced/read-only на период rollback.
4. Выполняйте только проверенный replay или compensating transaction со стабильными
   external ID и двойным контролем. Сообщите merchant точный интервал и исправление.
5. Закрытие: измерены RPO/RTO, подписана сверка, новая primary имеет рабочий backup,
   тревоги протестированы, старые credentials отозваны, evidence сохранён.

Тренировка не пройдена, если недоступны ключи, есть WAL gap, target не изолирован,
схема не запускает закреплённый release, ledger не балансируется, отличаются unique/
idempotency constraints, не доказана непрерывность scanner, цели сорваны без принятого
риска или отсутствуют два независимых подтверждения.
