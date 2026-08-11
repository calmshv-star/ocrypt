# JetStream 事件投递运维

PostgreSQL 始终是事件事实来源。JetStream 只是可选的至少一次投递辅助；
商户通过 `GET /v1/events` 恢复时仍读取 PostgreSQL，绝不静默切换到消息代理。

只有管理员预配 `MERCHANT_EVENTS_V1` 后才能启用 outbox worker。其 subject
固定为 `merchant.events.v1`，生产至少三副本，并设置有限的保留时间、字节数、
消息数和 1 MiB 单消息上限，采用 discard-old，去重窗口不得小于最大重试时间，
且禁止 Delete/Purge。subject 中不得包含租户 ID。

Worker 仅接受 `tls://`：强制 TLS 1.3、固定 CA 与服务器名、客户端证书，并且
只能挂载 credentials 文件或 token 文件之一。4222 端口和健康接口保持私有。
Readiness 会验证 PostgreSQL、NATS 连接及精确的 stream 策略。故障时让数据库
记录继续等待，不得切换传输方式。只有正确 stream 且 sequence 非零的 ack
才能标记已发布；重试始终复用 `Nats-Msg-Id=event_id`。

独立的参考 pull consumer 必须先原子提交唯一 inbox event_id 和业务效果，再发送
确认 ack；重复提交视为成功。发布前必须保留 TLS 失败、策略漂移、ack 丢失、
背压、发布 ack 后数据库失败及 redelivery 的真实环境证据。本地测试不代表
已验证真实集群。
