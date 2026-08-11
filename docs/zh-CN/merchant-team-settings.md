# 商户团队与项目设置

本模块同时按 tenant 和 merchant 隔离，仅管理人员访问与非财务偏好。汇率、资产、链、finality、matching、结算及资金库仍由独立的平台/财务控制面管理。

浏览器不会直接访问私有服务，也不能提交 actor、tenant、merchant、权限或审批人。Admin BFF 验证 OIDC 会话、已验证邮箱、issuer/subject、当前成员关系及 MFA，然后签发一次性、最长一分钟的 `MerchantSettingsAdmin` 断言；断言绑定 HTTP 方法、规范化 path/query 与请求体 SHA-256。BFF 与 API 之间强制 mTLS，API 还会从 PostgreSQL 重新读取权限。

## 角色与双人审批

`owner` 拥有全部权限；`security_admin` 管理团队安全；`admin` 管理普通成员和设置；`developer` 可读团队并修改设置；`support` 与 `viewer` 只读。角色由系统固定，客户端不能自定义权限。授予或移除 `owner`/`security_admin`，以及禁用或删除持有这些角色的成员，必须创建持久审批请求，并由另一名仍有权限、使用不同会话且完成新鲜 MFA 的人员审批。禁止自批和修改自己的角色。系统在同一 serializable 事务中重新校验 payload hash、成员版本、双方身份和权限、会话、MFA 时效与请求有效期。即使并发操作，数据库也禁止失去最后一个活跃 owner。

## 邀请

邀请中只能直接指定 `admin`、`developer`、`support`、`viewer`。邀请只能在到期前使用一次，接受者必须通过 OIDC 登录且已验证、规范化邮箱与邀请一致。新成员绑定服务端 admin user、issuer、subject 与邮箱；角色授予者记录为邀请人，而不是接受者。

邀请专用的同源 POST 会先规范解码 43 字符 token、计算 SHA-256 并查询邀请，再启动 OIDC；原始 token 不会进入数据库、日志、state、return path、cookie 或发送给 BFF 的 URL。浏览器会立即清除投递 fragment，只在当前标签页的 `sessionStorage` 中保留 token。流程强制 state、nonce、PKCE、MFA、精确 issuer/subject、`email_verified` 和受邀邮箱匹配。新 OIDC 身份只会创建为经审计、零平台权限的 `invited` 身份；其会话无法访问 `/session/me` 或其他 API，只能接受绑定的邀请。接受邀请、创建成员、激活身份和提升同一会话在一个 PostgreSQL 事务中完成。未完成且过期的注册会撤销会话并保持惰性；只有原用户的同一会话可用相同幂等键恢复丢失的响应。

`copy_once` 原子激活，并仅在首次响应显示 43 字符 token；幂等重放不会再次显示。`email` 会建立持久投递任务，只有 worker heartbeat 新鲜且所有待处理 key ID 均已加载时才允许。Worker 使用 invitation ID 作为邮件服务的幂等键，采用 lease 和有界退避重试，仅在持久 ACK 后激活；过期与 dead letter 都写入审计。

PostgreSQL 只保存 token 的 SHA-256 和非秘密 key ID。token 按 `HMAC-SHA256(delivery_key, "merchant-invite-v1\0" || tenant_id || merchant_id || invitation_id)` 派生，delivery key 不与 API、断言、webhook 或加密密钥复用。轮换时先加入新 key 并设为 current，等待旧 key ID 的待投递任务清空，再移除旧 key；提前移除会导致启动失败。某个 delivery key 与 invitation ID 清单同时泄露时，只影响该 key 下尚未 expiry/revoke 的邀请，不会泄露 API 或财务秘密。

禁用/删除成员会写入持久信号；最小权限 worker 原子撤销活跃 admin 会话并确认信号，崩溃后可重试。版本化设置包括显示名、`en/zh-CN/es/fr/de/ru` locale、IANA 时区、可选支持邮箱、通知偏好以及最多 100 个精确 HTTPS embed origin。禁止 HTTP、通配符、凭据、path、query 和 fragment。未知/重复 JSON 字段、秘密及财务策略均被拒绝。每次更新必须提供当前 version 和原因，并创建不可变快照及 SHA-256 审计链。

私有 API 使用 TLS 1.3 `:8447`，health 为 `:9095`；会话撤销 worker 为 `:9096`；邮件投递 worker 为 `:9097`。邮件模式默认 fail-closed。不得记录断言、邀请 token、bearer、key ring、OIDC token 或完整供应商响应。精确契约见 `contracts/merchant-settings-openapi.yaml`；所有变更都要求 `Idempotency-Key`。
