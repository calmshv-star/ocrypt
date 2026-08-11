# 保留策略与不可变归档

保留 worker 是故障关闭的归档数据平面，不删除业务记录。它把确定性、Ed25519 签名的归档写入启用版本控制和 `COMPLIANCE` Object Lock 的 S3 bucket；只有在精确核对对象版本、字节长度、SHA-256 与保留期限后，才会在 PostgreSQL 中登记不可变证据。

封闭的数据类别为 `callback_event_body`、`published_outbox_payload` 和 `event_history_payload`。callback 正文和事件历史目前只归档、不裁剪，因为现有重放/商户查询仍依赖热数据。只有已发布 outbox payload 在 `event_history` 中存在完全一致的副本时，才可替换为 `retention-tombstone/v1`。记录 ID、tenant/merchant 范围、类型、序列、时间、原始摘要和 WORM 对象引用都会保留。

裁剪必须同时满足：生效的租户版本策略、数据年龄、已验证 WORM 证据、无有效 tenant/merchant/record 法律保留、无待处理投递或重放依赖、有效 lease/fence，以及间隔 grace 周期的两次成功检查。最终 SERIALIZABLE 事务会在法律保留变更使用的同一 advisory lock 下重新执行所有检查。

worker 必须配置 `APP_ENV=production`、`RETENTION_OBJECT_STORE=s3`、唯一的 `RETENTION_WORKER_ID`、专用数据库 URL，以及文件挂载的 S3 凭据和 Ed25519 私钥。`/healthz` 与 `/readyz` 仅供私网使用。缺失 schema/权限、Object Lock/版本控制未通过准入或 lease 过期时，readiness 会失败。

迁移 000022 增加租户级控制面，且不授予直接表 DML。策略请求固定预期版本与 fence，必须由另一名完成近期 MFA 的操作员批准，并且不会早于数据库计划时间激活。callback 与事件历史始终只归档；outbox 裁剪仍需第二个 grace 周期。存在有效 hold 或已准入 Object Lock 时不能降低保留底线。

具备 `retention:hold_create` 和近期 MFA 后，法律保留立即生效。系统只接受 tenant、merchant、record 三种范围中的一种，并且不会泄露跨租户记录是否存在。释放必须先请求，再由不同于请求者和 hold 创建者的近期 MFA 审批人批准。到达 `expires_at` 并不会自动失效；worker 必须显式记录经审计的过期转换。浏览器只显示受限案件引用、状态、摘要、批次数量和 tombstone 身份，不返回正文、payload、对象 key/version 或凭据。

精确权限为 `retention:read`、`retention:policy_request`、`retention:policy_approve`、`retention:hold_create`、`retention:hold_release`。缺少 000022 或必要 EXECUTE 能力时 `/readyz` 会故障关闭。API 与 worker 均不得直接 `UPDATE`/`DELETE` 源数据或证据表。

PUT 响应丢失后，只接受完全匹配的不可变 HEAD 证据；版本、摘要、长度、锁模式或期限不符都会阻止确认和裁剪。回滚无法凭空恢复已经 tombstone 化的 payload，因此在完成单独审查的归档恢复前必须保留 000015。
