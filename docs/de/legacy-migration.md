# JSON-MD5-/Form-MD5-Migration

Der Legacy-Adapter ist eine vorübergehende und standardmäßig deaktivierte Brücke. Er erstellt normale Core-Intents und -Routen, liest Status über die PostgreSQL-gestützte Core-API und sendet Legacy-Callbacks erst nach `payment.settled`. Er kann weder „bezahlt“ setzen noch das Ledger ändern.

Vor dem Zulassungsantrag ist Migration `000018_legacy_compatibility` anzuwenden.

Vor der Freigabe werden ein Core-HMAC-Schlüssel mit `payments:read`, `payments:write`, `events:read`, dateibasierte HMAC-/MD5-Geheimnisse, ein HTTPS-Callback auf Port 443 und eine eindeutige Currency/Token/Network-Zuordnung benötigt. Zwei getrennte Operatoridentitäten müssen die 30 Minuten gültige Zulassung beantragen und genehmigen.

Signiert werden sortierte, nicht leere `key=value` plus Secret; JSON-MD5 lässt `signature`, Form-MD5 `sign` und `sign_type` aus. `trade_id` ist eine 128-Bit-Fähigkeit. Statusabfrage ist nur Wiederherstellung. Geschäftliche Gutschriften müssen idempotent sein; die Antwort lautet exakt kleingeschrieben `ok` oder `success`.

Vor dem `Sunset`-Datum auf Core-HMAC und kanonische Webhooks umstellen. Abgelaufene Zulassung, Ereignislücke, fehlendes Secret oder TLS-Fehler schließen Readiness. Dieses Repository belegt keinen Live-Betrieb.
