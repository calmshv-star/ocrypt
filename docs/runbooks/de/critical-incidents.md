# Verfahren für kritische Vorfälle

## Allgemeine Kontrollen

1. Schweregrad, UTC-Start, Incident Commander, Operator und Protokollführung benennen;
   bei Bedarf Security ergänzen. Nur einen auditierbaren Incident-Kanal verwenden.
2. Releases, Migrationen, Schlüsselrotation, Backfills und manuelle Zuordnungen
   stoppen. Letzte funktionierende Image-Digests und Konfigurations-Hashes festhalten.
3. Umgebung, betroffene Tenant-Anzahl, Chain/Asset, Zeitraum und Zustandsübergang
   dokumentieren. Keine Secrets, Callback-Bodies, Wallet-Adressen oder Personendaten posten.
4. Reversibel isolieren: Gateway-Traffic entfernen, nur betroffenen Worker auf null
   skalieren oder dessen Egress sperren. Keine Zeilen löschen, Cursor verschieben,
   „paid“ erzwingen oder Callbacks per Ad-hoc-SQL wiederholen.
5. Health/Readiness, Deployment-Events, bereinigte Logs, Queue-Alter, Provider-Heads,
   DB-Zustand und Release-Historie sichern. Saldo-/Berechtigungsänderungen benötigen
   Vier-Augen-Prüfung und eine geschützte append-only Liste betroffener IDs.
6. Erst nach Backlog-Abbau, Invariantenprüfung, Kundenkorrektur, Alarmtest,
   Beweissicherung und Zuweisung der Abstellmaßnahmen schließen.

## API oder Worker nicht verfügbar

1. `/healthz` und `/readyz` vergleichen: alive, aber nicht ready deutet meist auf DB
   oder Credentials; beide fehlerhaft auf Prozess, Image, Scheduling, Speicher oder Node.
2. Soll-/Ready-Replikas, Restarts, PDB, Pending Pods, Existenz der Secret-Keys ohne
   Werte, Pool, TLS, DNS und letzte NetworkPolicy-Änderungen prüfen.
3. Bei Release-Fehler nur das betroffene Deployment auf den dokumentierten Digest
   zurücksetzen. Schema nicht zurückrollen. Bei DB-Zweifel Writers stoppen und Restore folgen.
4. Vor schrittweiser Öffnung Readiness, signierte Anfrage, idempotente Intent-Anlage,
   Settlement-Fortschritt und einen kontrollierten Callback prüfen.

## Scanner-Rückstand oder Provider-Abweichung

Gilt erst, nachdem der Scanner sein Produktions-Gate bestanden hat.

1. Scanner und Settlement auf null skalieren; Callbacks bereits verbuchter Events
   weiterlaufen lassen. Cursor nicht vorsetzen und Finality nicht absenken.
2. Für jedes RPC Identität, Chain/Genesis, Finalized Head, Hash der letzten gemeinsamen
   Höhe, Latenz und Fehlerklasse sichern. Explorer-Screenshot oder Beleg reichen nicht.
3. Einen Provider nur entfernen, wenn die verbleibenden wirklich unabhängig sind
   und Quorum halten. Letzte per Quorum konsistente Hash/Parent-Höhe bestimmen.
4. Überlappend in isolierten Vergleichsspeicher neu scannen. Transfer-Eindeutigkeit,
   Contract/Mint-Allowlist, Decimals, Empfänger, Logs/interne Calls/Instruktionen und
   Bestätigungsschwelle abgleichen.
5. Erst Canary-Shard, dann Settlement starten; ältestes Queue-Alter und Duplikate
   beobachten. Unmatched oder nur durch KI vorgeschlagene Kandidaten nie automatisch gutschreiben.

## Chain-Reorganisation

1. Scanner und Settlement der Chain pausieren. Alte/neue Hashes, gemeinsamen Vorfahr,
   Tiefe, Quorum und betroffene Event-IDs festhalten.
2. Prüfen, ob das verwaiste Event Finality überschritt und Ledger/Berechtigung änderte;
   beide Historien behalten. Observations nicht löschen und Ledger nicht editieren.
3. Den vorgesehenen Reorg/Reversal-Fluss nutzen. Finanzkorrekturen sind neue,
   verknüpfte, ausgeglichene Kompensationsbuchungen unter Vier-Augen-Kontrolle.
4. Vor dem gemeinsamen Vorfahr neu scannen, abgleichen und erst dann fortsetzen.
   Merchant mit stabilen IDs und Korrekturstatus informieren, ohne Irreversibilität zu versprechen.

## Callback-Ausfall

1. Korrektes Settlement bestätigen: Callback ist Benachrichtigung, nicht Wahrheit.
   Älteste ausstehende Lieferung und betroffene Endpoints messen.
2. Readiness, Leases, DNS/HTTPS-Egress, Zertifikate, Response-Klassen und Envelope-Key-
   Entschlüsselung prüfen, ohne credentialisierte URLs oder Bodies offenzulegen.
3. Nach Behebung die durable Queue mit ursprünglichen Event-IDs/Signaturen wiederholen
   lassen. Keine Duplikate erzeugen und SSRF nicht umgehen. Pro Endpoint drosseln,
   Backoff einhalten und Empfänger-Deduplizierung bestätigen.

## Anstieg nicht zugeordneter Zahlungen

1. Automatisches und manuelles Matching oberhalb des genehmigten Grenzwerts stoppen,
   Ingestion-Beweise behalten. KI berät nur und genehmigt keine Gutschrift.
2. Nach Chain, Contract/Mint, Empfänger, Mechanismus, Decimals, Zeit und Route-Version
   segmentieren; Adresspool-Erschöpfung und Assignment-Ablauf prüfen.
3. Normalisierung mit zwei unabhängigen RPCs und Contract/Exchange-Fixtures validieren.
   Ein Screenshot ist kein Nachweis.
4. Parser/Route vorwärts korrigieren und in Vergleichsspeicher replayen. Manuelle
   Resolution verlangt Vier-Augen-Prüfung sowie bewiesene Idempotenz und Eindeutigkeit.

## Credential- oder Schlüsselverlust

1. Als kompromittiert behandeln und nicht erneut anfordern. An der Autorität sperren
   und Workload isolieren. Auth-Logs, Secret-Audit, Flows, Digest und Historie sichern.
2. Minimal berechtigten Ersatz ausstellen, externes Secret aktualisieren, nur den
   betroffenen Workload rollen und Ablehnung des alten Werts bestätigen.
3. Envelope Key nicht blind ersetzen: versionierte Entschlüsselung, verifiziertes
   Neuverschlüsseln jeder Zeile und Rollback-Key unter Vier-Augen-Kontrolle sind nötig.
   Bei Unsicherheit zuerst Snapshot erstellen.
4. Unautorisierte Intents, Resolutions, Callbacks und Ledger-Übergänge prüfen;
   kompensieren statt Historie löschen und gemäß Security/Legal-Prozess melden.

## DB-Korruption oder Split-Brain

1. Writes entfernen, API/Scanner/Workers stoppen und verdächtigen Primary fencen.
   Nie zwei schreibbare Primaries zulassen oder Storage wiederholt neu starten.
2. Logs, WAL, Timelines, Snapshots, Chain-Beweise und letzten verlässlichen UTC-Zeitpunkt sichern.
3. In neue isolierte DB restaurieren, abgleichen und kontrolliert umschalten. Nie die
   einzige Kopie überschreiben oder während des Vorfalls Zeilen improvisiert reparieren.
