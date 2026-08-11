# PostgreSQL-Backup und -Restore

## Ziel und Verantwortung

Intents, Chain-Observations, Idempotenz, Matching, Ledger, Outbox und Scanner-Cursor
schützen. DB Owner betreibt Backups; Incident Commander genehmigt Produktions-Restore;
Risk/Finance zeichnet den Abgleich ab; Security kontrolliert Restore-Credentials.
Ziele: RPO höchstens 5 Minuten und RTO 4 Stunden, sofern kein strengeres Ziel gilt.

Erforderlich sind verschlüsseltes kontinuierliches WAL/PITR, täglicher logischer
Export, verschlüsselte unveränderliche Kopie in anderem Konto/Fehlerbereich,
Überwachung von Retention/Frische/WAL-Gaps/Checksums, getrennte Runtime-/Migration-/
Backup-/Restore-Identitäten, vierteljährlicher isolierter Restore und jährliche
Regionsübung. „Backup enabled“ ist kein Beweis: Zeiten, WAL-Kontinuität, Checksums,
Key-Recovery, Dauer, Abgleich und zwei Freigaben aufbewahren.

## Entscheidung vor dem Restore

1. Incident erklären, Releases/Migrationen stoppen und Ausfall, Korruption,
   Kompromittierung oder Langsamkeit unterscheiden. Bei Integritätsrisiko Writes
   fencen und letzten vertrauenswürdigen UTC-Zeitpunkt samt Begründung festhalten.
2. Logs, Timelines, WAL, Snapshots, Chain-Beweise und Digests sichern. PITR bei
   Löschung/Fehlbedienung, logischen Restore zur unabhängigen Validierung wählen.
3. Nie über die aktuelle DB restaurieren. Neues isoliertes Ziel ohne App-Netzpfad
   erstellen. Zweite Person genehmigt Ziel, Zeitpunkt, Backup, Keys und Verlustfenster.

## Restaurieren und validieren

1. Mit Provider-Tooling in neue Instanz/neues Konto restaurieren: privates Netz, TLS,
   Verschlüsselung, Audit und temporäres Restore-Credential. Apps/Callbacks sperren.
2. Für logisches Archiv passende PostgreSQL-Tools nutzen, beim ersten Fehler stoppen,
   Version/Ausgabe sichern und keine Rechte erweiternden Owner/ACL übernehmen. Nur
   kompatible Forward-Migrationen, niemals Down-Migrationen ausführen.
3. Integrität, Constraints/FK, Extensions/Collations, RLS enabled/forced und minimale Grants prüfen.
4. Mindestens abgleichen:
   - Anzahl/Status von Intents und Routes pro Tenant/Zeit;
   - Eindeutigkeit von Transfer Identity und aktiven Payment Matches;
   - Summe null aller Entries jeder Ledger Transaction;
   - Idempotency Keys und Response Hashes;
   - Unmatched/Manual Resolutions und Freigebende;
   - Callback/Outbox-Identität, Reihenfolge, Attempts und Endstatus;
   - Scanner-Cursor/Gap-Kontinuität gegen unabhängige Finalized Chain;
   - Assignments/Reservations ohne kollidierende aktive Leases.
5. Verlorenes RPO-Fenster mit unveränderlichen Beweisen vergleichen und genehmigte
   Replay-/Kompensationsliste erstellen. Keine Massengutschrift nur nach Betrag,
   Zeit, Screenshot oder KI-Score.

## Umschaltung

1. Neue minimal berechtigte Runtime-Credentials erstellen, externe Secrets
   aktualisieren und aus isolierten Canary Pods testen. Restore-Credential nicht wiederverwenden.
2. API bei geschlossenem Ingress starten, danach Settlement, Callback und Scanner
   getrennt. Scanner bleibt ohne Release-Gate aus. Signierte/idempotente API, Queue
   und kontrollierten Callback testen.
3. Traffic schrittweise öffnen und Readiness, DB-Fehler, Queue-Alter, Duplikate,
   Quorum und Callback-Fehler beobachten. Alte DB im Rollback-Fenster fenced/read-only halten.
4. Nur geprüfte Replays/Kompensationen mit stabilen IDs und Vier-Augen-Kontrolle
   ausführen. Merchant über genaues Fenster und Abhilfe informieren.
5. Erst nach RPO/RTO-Messung, signiertem Abgleich, geprüftem Backup des neuen Primary,
   Alarmtest, Credential-Widerruf und Beweissicherung schließen.

Die Übung ist fehlgeschlagen, wenn Keys fehlen, WAL eine Lücke hat, das Ziel nicht
isoliert ist, das fixierte Release nicht startet, das Ledger unausgeglichen ist,
Unique-/Idempotenz-Constraints abweichen, Scanner-Kontinuität unbelegt ist, Ziele ohne
akzeptiertes Risiko verfehlt werden oder zwei unabhängige Freigaben fehlen.
