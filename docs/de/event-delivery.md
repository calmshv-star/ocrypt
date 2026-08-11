# Betrieb der JetStream-Ereigniszustellung

PostgreSQL bleibt die maßgebliche Ereignisquelle. JetStream ist nur eine
optionale At-least-once-Zustellhilfe; `GET /v1/events` stellt weiterhin aus
PostgreSQL wieder her und weicht nie auf den Broker aus.

Aktivieren Sie den Outbox-Worker erst nach der administrativen Bereitstellung
von `MERCHANT_EVENTS_V1` für das feste Subject `merchant.events.v1`: mindestens
drei Replikate, endliche Alters-, Byte-, Nachrichten- und 1-MiB-Grenzen,
Discard-Old, ein Deduplizierungsfenster über der maximalen Wiederholungszeit
sowie gesperrtes Delete/Purge. Mandanten-IDs gehören nicht in Subjects.

Der Worker akzeptiert ausschließlich `tls://` mit TLS 1.3, festgelegter CA und
Servername, Client-Zertifikat und genau einer eingebundenen Credentials- oder
Token-Datei. Port 4222 und Health-Endpunkte bleiben privat. Readiness prüft
PostgreSQL, NATS und die exakte Stream-Richtlinie. Bei Ausfall bleiben Einträge
in PostgreSQL ausstehend; Transport nicht umschalten. Erst ein Ack mit richtigem
Stream und Sequenz markiert veröffentlicht. Wiederholungen behalten
`Nats-Msg-Id=event_id`.

Der Referenz-Pull-Consumer bestätigt erst nach atomarem Inbox-/Geschäftscommit;
Duplikate sind erfolgreich. Vor Freigabe sind Live-Nachweise für TLS-Fehler,
Policy-Drift, verlorenes Ack, Backpressure, DB-Fehler nach Ack und Consumer-
Redelivery erforderlich. Lokale Tests sind kein Cluster-Nachweis.
