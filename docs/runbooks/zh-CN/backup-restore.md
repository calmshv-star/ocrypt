# PostgreSQL 备份与恢复

## 目标与职责

保护 intent、链 observation、幂等记录、匹配、账本、outbox 和扫描游标。数据库
负责人运营备份；事件指挥批准生产恢复；风险/财务签署对账；安全团队控制恢复
凭据。除非有更严格目标，RPO 不超过 5 分钟，RTO 为 4 小时。

必须具备：连续加密 WAL/PITR、每日逻辑导出用于独立验证、另一账户或故障域内
不可变加密副本、受监控的保留期/新鲜度/WAL 缺口/校验和、分离的 runtime/
migration/backup/restore 身份、季度隔离恢复及年度区域故障演练。“已启用备份”
不是证据；应保留时间戳、WAL 连续性、对象校验和、密钥恢复、恢复耗时、对账
结果及两名审批人签名。

## 恢复前决策

1. 宣布事件并停止发布/迁移，判断源是不可用、损坏、遭入侵还是仅变慢。完整性
   或 split-brain 存疑时 fence 写入，并记录最后可信 UTC 时间及依据。
2. 保存日志、timeline、WAL、snapshot、链证据和当前镜像/配置摘要。误删/误操作
   选择 PITR；独立验证选择逻辑恢复。
3. 永远不要覆盖当前数据库。创建与应用网络隔离的新目标。第二名审批人确认目标、
   恢复点、备份集、密钥和预期数据损失窗口。

## 恢复与验证

1. 用提供商工具恢复到新实例/账户；启用私网、TLS、加密、审计和临时恢复凭据，
   禁止应用访问与外部回调。
2. 逻辑归档使用匹配的 PostgreSQL 工具，首个错误即停止，保存工具版本和输出；
   不恢复会扩大权限的 owner/ACL。只执行兼容的向前迁移，不执行 down migration。
3. 验证 PostgreSQL 完整性、constraint/FK、extension/collation、必要表的 RLS 已
   enabled/forced，以及最小权限 grant。
4. 至少对账：
   - 按租户/时间统计 intent 与 route 的数量和状态；
   - transfer identity 与 active payment match 唯一；
   - 每个 ledger transaction 的 entry 总和为零；
   - idempotency key 与 response hash；
   - unmatched/manual resolution 及审批人；
   - callback/outbox 身份、顺序、attempt 和终态；
   - scanner cursor/gap 与独立 finalized chain 连续；
   - address assignment 与 amount reservation 无冲突 active lease。
5. 将 RPO 丢失区间与不可变链/提供商证据比较，形成获批的 replay/compensation
   清单。不得仅凭金额、时间、截图或 AI 分数批量入账。

## 切换

1. 创建新的最小权限 runtime 凭据，更新外部 Secret，并从隔离 canary Pod 测试；
   不复用 restore 凭据。
2. 在入口关闭时启动 API，再分别启动 settlement、callback、scanner；scanner 未
   通过发布门禁时保持关闭。执行签名/幂等 API、受控队列和回调测试。
3. 逐步开放流量，监控就绪、数据库错误、队列年龄、重复、quorum 与回调故障。
   旧数据库在回滚/取证期保持 fenced、只读。
4. 仅执行经过复核、具有稳定 external ID 和双人控制的 replay/补偿；向商户说明
   准确影响区间和修复结果。
5. 关闭条件：RPO/RTO 已测量、对账签署、新主库备份已验证、告警已测试、旧凭据
   已撤销、证据已保留，并安排整改复盘。

若恢复密钥不可用、WAL 有缺口、目标未隔离、固定版本无法启动、账本不平衡、
唯一/幂等约束不同、扫描连续性无法证明、未获风险接受而错过目标，或缺少两名
独立审批人，则演练失败。
