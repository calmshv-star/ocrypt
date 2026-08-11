# 确定性商户沙盒

沙盒是独立的测试产品，不是 live 支付引擎的开关。只有在 `APP_ENV=sandbox|test`、`SANDBOX_RUNTIME=postgres`、专用数据库以及 `mk_test_` 测试凭证同时存在时，系统才注册 `/v1/sandbox/*`。生产环境和普通开发环境返回 `404`，PostgreSQL 还会再次拒绝 live 商户。

先调用 `GET /v1/sandbox/workspace`，获取测试时钟、版本、已脱敏的凭证元数据、确定性测试地址，以及绑定版本的 HMAC reset 令牌。`POST /v1/sandbox/scenarios` 会创建仅存在于沙盒的 payment intent 和 route；这些 UUID 无法通过 `/v1/payment-intents` 读取。所有金额均为精确整数字符串。

支持 `exact_payment`、`partial_payment`、`underpayment`、`overpayment`、`late_payment`、`wrong_asset`、`duplicate_callback`、`out_of_order_callback`、`timeout`、`dead_letter`、`reorg` 和 `reorg_recovery`。通过 `POST /v1/sandbox/scenarios/{id}/actions` 可逐步模拟 observation、confirmations、finality、callback、reorg 和 recovery。未达到所需确认数且未执行明确 finality 时不能结算；reorg 后必须重新纳入观察、重新确认并再次达到终局。`POST .../{id}/run` 会原子且幂等地执行模板。

`GET /v1/sandbox/callbacks` 使用游标分页返回规范 JSON、SHA-256、受限尝试次数和状态。密钥、响应正文及错误文本不会被保存或展示，只保留封闭的错误类别和字节数。Reset 必须提供幂等键、当前 workspace 版本和 HMAC 令牌，并且只删除该商户的 `sandbox_*` 数据。

通过沙盒不等于获准上线。真实 provider、finality/reorg、恢复、密钥轮换、固定制品和负载测试仍是独立门槛。AI 不参与观察、确认或结算。
