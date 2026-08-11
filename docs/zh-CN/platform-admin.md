# 平台配置管理

Platform Admin API 是内部配置控制平面，管理租户、项目/环境、网络、资产合约版本、只读钱包/地址池、RPC 能力、汇率源与策略、finality/matching 策略、配额、通知引用、功能开关和维护窗口。系统绝不接收私钥、助记词、API secret 或 provider credential。

API 仅允许内部 TLS。浏览器只能访问同源 Admin BFF，assertion key 不会下发到 React。BFF 校验服务端会话、Origin/CSRF、MFA 和数据库 RBAC，然后签发一次性 assertion；签名绑定 HTTP 方法、escaped path、规范 query、精确 body hash、audience、过期时间和 nonce。浏览器 JSON 不能声明 grants。

权限为 `platform_config:read|write|request|approve|schedule|activate|rollback|emergency`。Auditor 只能读取；Security Admin 创建/提交变更并执行紧急操作；Senior Approver 必须与申请人不同，负责批准、排期、激活和 rollback draft。Tenant scope 不会扩展到其他租户。

流程为 `draft → approval_requested → approved/rejected → scheduled → active`。激活只追加不可变 snapshot/activation history，并原子更新带 fence token 的 head。Scheduler 使用 `SKIP LOCKED`、有界 lease 和单调 claim token。Rollback 会创建新版本并重新审批，不改写历史。没有直接编辑、`PUT` 或 `PATCH`。紧急暂停/恢复是单独审计的 append-only 事件流。

精确金额和 quota limit 必须是十进制字符串。系统校验各链合约格式、汇率 quorum/staleness、无 credential 的 HTTPS endpoint、watch-only 引用及维护窗口。RLS/FORCE、复合外键、绑定 path/body 的幂等指纹和 audit hash chain 防止跨租户与竞态。

Rate worker 继续使用 `ActiveSnapshotReader`。Scanner 在每个周期通过 `RuntimeStateReader` 的单个 serializable transaction 同时读取 chain、finality、全部 RPC/asset、maintenance 以及最新 emergency pause。缺失、无 fence、暂停或交叉引用不一致都会 fail closed；confirmations 会从 quorum head 扣除，并要求 `overlap >= reorg_depth`。每次提交范围都会追加不可变的 `scanner_runtime_config_evidence`。RPC secret 只由 `credential_ref` 指向 `SCANNER_SECRET_DIR` 下的外部文件；production 的 `SCANNER_PLATFORM_RUNTIME_JSON` 使用 `{"chain":"eip155:1","finality":"eip155:1","rpc_providers":["rpc/one","rpc/two"],"assets":["eth-ethereum","usdt-ethereum"],"maintenance":["eip155:1"]}` 结构，静态配置仅限 development/test。

Route planner 在选择 rate tick 和租用地址的同一事务内执行 admission。约定键为：tenant environment=merchant UUID，global chain/finality=chain ID，global asset=asset ID，tenant wallet pool=wallet UUID。所有资源必须 active/current；tenant `new_routes` flag 也必须存在、enabled 且 rollout 为 10000 bps。flag 缺失/禁用、emergency pause 或有效的 `read_only`/`disable_new_routes` 窗口都会拒绝新 route。Quote/assignment 保存 snapshot ID 和 fence 证据。Matching 仍由独立 four-eyes policy 控制；quota、notification channel 和金融 sweep/refund finality 尚未接入，不能仅因 activation 就视为生效。

`platform-outbox-publisher` 使用 enabled `outbox_publisher` identity 和 fenced claim，通过 TLS 1.3、mTLS、文件 bearer token 向固定 `/v1/platform-admin/events` 发送。只有响应同时匹配 `event_id` 与 `claim_token` 才标记 published；redirect/proxy 被禁止，health 仅绑定 loopback。真实 PostgreSQL/provider/destination 互操作仍是部署准入检查。
