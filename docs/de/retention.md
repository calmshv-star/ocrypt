# Aufbewahrung und unveränderliche Archive

Der Retention-Worker ist eine fehlersichere Archiv-Datenebene. Er löscht keine Geschäftsdatensätze. Er schreibt deterministische, mit Ed25519 signierte Archive in einen versionierten S3-Bucket mit Object Lock im Modus `COMPLIANCE` und bestätigt erst danach Objektversion, Byte-Länge, SHA-256, Aufbewahrungsdatum und unveränderliche Datenbanknachweise.

Die geschlossenen Datenklassen sind `callback_event_body`, `published_outbox_payload` und `event_history_payload`. Callback-Inhalte und Ereignisverlauf werden nur archiviert, da Wiedergabe und Händlerabfragen weiterhin die aktiven Bytes benötigen. Nur ein bereits veröffentlichter Outbox-Inhalt darf durch `retention-tombstone/v1` ersetzt werden, wenn eine exakt gleiche Kopie im Ereignisverlauf existiert. Identitäten, Mandantenbereich, Typen, Sequenzen, Zeiten, Original-Hash und Objektverweis bleiben erhalten.

Eine Bereinigung erfordert eine wirksame versionierte Mandantenrichtlinie, ausreichendes Alter, geprüften WORM-Nachweis, keinen aktiven Legal Hold, keine ausstehende Zustellung oder Wiedergabe, einen gültigen Lease/Fence und zwei erfolgreiche Prüfungen mit der konfigurierten Schonfrist. Die letzte SERIALIZABLE-Transaktion prüft alles erneut unter derselben Advisory-Sperre wie Änderungen an Legal Holds.

Erforderlich sind `APP_ENV=production`, `RETENTION_OBJECT_STORE=s3`, eine eindeutige `RETENTION_WORKER_ID`, eine eigene Datenbank-URL sowie gemountete S3- und Ed25519-Schlüsseldateien. `/healthz` und `/readyz` bleiben privat. Readiness schlägt bei fehlendem Schema/Rechten, fehlender Object-Lock-/Versionierungsfreigabe oder veralteten Leases fehl.

Migration 000022 ergänzt die mandantenbezogene Operator-Control-Plane ohne direkte Tabellenänderungsrechte. Ein Richtlinienantrag bindet erwartete Version und Fence, benötigt die Freigabe einer anderen Person mit frischer MFA und wird frühestens zur geplanten Datenbankzeit aktiviert. Callback-Inhalte und Ereignisverlauf bleiben immer archive-only; Outbox-Bereinigung behält die zweite Grace-Prüfung. Eine niedrigere Aufbewahrung wird bei aktivem Hold oder bereits zugelassenem Object Lock abgelehnt.

Ein Legal Hold wird nach `retention:hold_create` und frischer MFA sofort wirksam. Tenant-, Merchant- oder Record-Scope wird ohne mandantenübergreifendes Existenzleck geprüft. Eine Freigabe benötigt immer Antrag und einen anderen frischen MFA-Freigeber, der auch nicht der Hold-Ersteller sein darf. Ablauf wird erst nach `expires_at` durch einen expliziten auditierten Worker-Schritt wirksam. Der Browser zeigt nur begrenzte Fallreferenzen, Status, Digests, Batch-Zahlen und Tombstone-Identität, niemals Payloads, Bodies, Objektschlüssel/-versionen oder Credentials.

Die exakten Rechte sind `retention:read`, `retention:policy_request`, `retention:policy_approve`, `retention:hold_create` und `retention:hold_release`. `/readyz` schließt bei fehlender Migration 000022 oder Capability. API und Worker erhalten kein direktes `UPDATE`/`DELETE` auf Quell- oder Evidenztabellen.

Nach einer verlorenen PUT-Antwort wird nur ein exakt passender unveränderlicher HEAD-Nachweis akzeptiert. Abweichungen stoppen Bestätigung und Bereinigung. Ein Rollback kann bereits tombstonte Inhalte nicht synthetisieren; Migration 000015 muss deshalb bestehen bleiben, bis eine separat geprüfte Wiederherstellung aus dem Archiv erfolgt.
