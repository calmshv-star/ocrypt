# 托管支付提供商

托管网关与链上路由使用同一套 payment-intent API。请求必须明确区分：`{"provider":"hosted_gateway","hosted_gateway":{"provider_id":"provider-account-1","asset_id":"usdt-tron"}}`。链上请求使用 `provider=on_chain` 和 `on_chain` 对象；混合、旧式或含糊的请求会被拒绝。

托管路由返回 `provider_id`、`provider_reference`、资产、精确整数金额以及服务器验证过的 HTTPS `payment_url`。系统不会伪造收款地址、链、确认数或交易哈希。法币到资产的报价、向上取整结果、时间和提供商响应摘要均以不可变证据保存，不使用浮点数。

Callback 只有在通过大小限制和 HMAC 验证后才写入 append-only inbox。相同事实的重复事件可安全重放；同一 event ID 携带不同事实会产生冲突；旧事件不会回退状态。若 callback 早于路由绑定到达，它会保存在有界 pre-bind inbox 中，并在 durable provider order 绑定后重放。

在 payment intent 锁内，第一个完成结算的已验证同级路由获胜；其他路由变为 superseded，但迟到证据仍会保留。提供商暂停时收到的 callback 会被隔离并创建 reconciliation incident，不会写入 ledger。Status/reconcile 响应和结算后的 refund 只能形成证据或 incident；自动 refund 执行仍未启用。

公开托管支付链接的兑换事务不会在事务内调用提供商。该事务会原子地占用一次使用次数，并创建 intent、checkout capability、不可变的 create attempt 与 preparation job。Checkout 在 fenced worker 绑定经验证的提供商路由前只返回 `preparing_payment_route`，不会返回地址或 URL。重放会返回同一 capability；过期或恢复耗尽会以持久 incident 进入 `payment_route_failed`，且不会静默恢复链接容量。

生产环境要求 `000016`、`000017`、`000019` 和 `000020` 迁移、精确权限、外部挂载的 HMAC 密钥、操作准入策略，以及 API/worker readiness。同源 platform-admin 控制台是唯一受支持的配置与轮换入口：不可变无密钥清单、仅写外部文件引用、另一名刚完成 MFA 的审批人，以及绑定响应和对端证书摘要的私有 TLS 探测。激活后提供商仍保持暂停；还必须审批新的六项操作策略并单独进行双人解除暂停。旧数据以 `legacy_unadmitted` 迁移，不伪造探测证据。不得使用 merchant API 或 DB owner 临时 SQL 配置提供商。
