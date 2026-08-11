# Deterministische Händler-Sandbox

Die Sandbox ist ein getrenntes Testprodukt und kein Live-Schalter. `/v1/sandbox/*` wird nur mit `APP_ENV=sandbox|test`, `SANDBOX_RUNTIME=postgres`, einer eigenen Datenbank und einem Testschlüssel mit `mk_test_` registriert. Produktion und normale Entwicklung liefern `404`; PostgreSQL weist Live-Händler zusätzlich zurück.

`GET /v1/sandbox/workspace` liefert Testuhr, Version, geschwärzte Zugangsdaten, deterministische Testadressen und das versionsgebundene HMAC-Reset-Token. `POST /v1/sandbox/scenarios` erzeugt einen ausschließlich in der Sandbox existierenden Payment Intent samt Route. Diese UUIDs sind über `/v1/payment-intents` nicht lesbar; Geldbeträge bleiben exakte Ganzzahl-Strings.

Verfügbar sind `exact_payment`, `partial_payment`, `underpayment`, `overpayment`, `late_payment`, `wrong_asset`, `duplicate_callback`, `out_of_order_callback`, `timeout`, `dead_letter`, `reorg` und `reorg_recovery`. Mit `POST /v1/sandbox/scenarios/{id}/actions` werden Beobachtung, Bestätigungen, Finalität, Callback-Ergebnis, Reorg und Recovery schrittweise simuliert. Eine Zahlung wird erst nach den erforderlichen Bestätigungen und einem Finalitätsschritt abgerechnet. Nach einem Reorg beginnt die Bestätigung der wieder aufgenommenen Beobachtung erneut. `POST .../{id}/run` führt die Vorlage atomar und idempotent aus.

`GET /v1/sandbox/callbacks` zeigt den kanonischen JSON-Body, SHA-256, begrenzte Versuche und Status mit Cursor-Paginierung. Geheimnisse sowie Antwort- und Fehlertexte werden weder gespeichert noch ausgegeben. Reset erfordert Idempotency-Key, aktuelle Workspace-Version und HMAC-Token und löscht ausschließlich händlereigene `sandbox_*`-Daten.

Sandbox-Erfolg ersetzt keine Produktionsnachweise für Provider, Finalität/Reorg, Restore, Schlüsselrotation, fixierte Artefakte oder Last. KI beobachtet, bestätigt und verbucht keine Zahlungen.
