# Регламент теневой миграции

Миграция `000021` сохраняет PostgreSQL источником истины для учёта. Сначала запустите offline-проверку `migration-control`: она работает только в dry-run. Экспортируйте ограниченный инвентарь без секретов, подпишите точные канонические байты двумя разными разрешёнными ключами Ed25519 и отправьте манифест через tenant-scoped admin API.

Переходы выполняются только через inventory, validation, раздельные request/approval/execution, import, shadow и canary. Cutover остаётся pending, пока отдельно аутентифицированный актуатор не подтвердит точные version и fence. Canary abort и rollback сохраняют наблюдения, бухгалтерские факты и ownership fences. Watch-only адреса нельзя освобождать, а shadow-наблюдения до cutover нельзя массово зачислять.

Verification Job по умолчанию имеет `MIGRATION_EXECUTE=false`. Запись требует отдельный worker login, lease/fence, взаимный TLS 1.3, разные provider hosts и quorum-подписанные факты. Decommission требует нулевой backlog из БД и неизменяемые доказательства archive, restore test и key revoke.

Локально не выполнялись live source DB, chain, PostgreSQL cutover, actuator, Helm cluster или provider quorum. Эти проверки должны быть приложены к release manifest.
