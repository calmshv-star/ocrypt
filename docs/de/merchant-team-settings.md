# Händlerteam und Projekteinstellungen

Das Modul ist nach Tenant und Händler isoliert. Es verwaltet ausschließlich menschliche Zugriffe und nichtfinanzielle Einstellungen. Kurse, Assets, Chains, Finality, Matching, Settlement und Treasury bleiben in getrennten Kontrollbereichen.

Der Browser ruft den privaten Dienst nie direkt auf und liefert weder Actor, Tenant, Händler, Berechtigungen noch Approver. Das BFF prüft OIDC-Session, verifizierte E-Mail, Issuer/Subject, Mitgliedschaft und MFA und signiert danach eine einmalige, eine Minute gültige `MerchantSettingsAdmin`-Assertion, gebunden an Methode, kanonischen Path/Query und Body-SHA-256. BFF und API verwenden mTLS; die API liest Rechte erneut aus PostgreSQL.

## Rollen und Vier-Augen-Prinzip

`owner` hat Vollzugriff; `security_admin` verwaltet Teamsicherheit; `admin` verwaltet normale Mitglieder und Einstellungen; `developer` liest das Team und ändert Einstellungen; `support` und `viewer` lesen. Rollen sind systemdefiniert, Clients können keine Permissions einspeisen. Vergabe/Entzug von `owner` oder `security_admin` sowie Sperren/Löschen ihrer Inhaber benötigen einen dauerhaften Antrag und einen anderen aktiven MFA-Akteur in einer anderen Session. Selbstfreigabe und eigene Rollenänderung sind verboten. Hash, Zielversion, Identitäten, Rechte, Sessions, MFA-Alter und Ablauf werden in einer serialisierbaren Transaktion erneut geprüft. Die Datenbank schützt den letzten aktiven Owner auch bei Parallelität.

## Einladungen

Direkt einladbar sind nur `admin`, `developer`, `support`, `viewer`. Die Einladung ist einmalig vor Ablauf durch eine OIDC-Identität mit passender verifizierter normalisierter E-Mail annehmbar. Das Mitglied wird an Admin User, Issuer, Subject und E-Mail gebunden; als Grantor gilt der Einladende.

Ein einladungsspezifischer Same-Origin-POST dekodiert das kanonische 43-Zeichen-Token, bildet SHA-256 und löst die Einladung vor OIDC auf. Das Roh-Token wird nie gespeichert oder protokolliert und gelangt weder in State, Return Path, Cookies noch in eine an das BFF gesendete URL. Der Browser entfernt das Delivery-Fragment sofort und hält das Token nur im `sessionStorage`. State, Nonce, PKCE, MFA, exakte Issuer/Subject-Werte, `email_verified` und passende Einladungs-E-Mail sind Pflicht. Eine neue Identität wird auditiert als `invited` ohne Plattform-Grants angelegt; ihre Session darf nur diese Einladung annehmen. Mitgliedschaft, Verbrauch, Aktivierung und Session-Promotion erfolgen atomar in einer PostgreSQL-Transaktion. Abgelaufene Anmeldungen bleiben inert und ihre Sessions werden widerrufen. Nur dieselbe Session kann bei verlorener Antwort denselben Idempotency Key wiederholen.

`copy_once` wird atomar aktiv und zeigt den 43-stelligen Token nur in der ersten Antwort; Replay enthält keinen Token. `email` erzeugt einen dauerhaften Job und wird nur bei frischem Worker-Heartbeat und vollständigem Key Ring zugelassen. Der Worker nutzt Invitation ID als Provider-Idempotency-Key, leased und wiederholt begrenzt und aktiviert erst nach dauerhaftem ACK. Ablauf und Dead Letter werden auditiert.

PostgreSQL speichert nur Token-SHA-256 und eine nicht geheime Key ID. Ableitung: `HMAC-SHA256(delivery_key, "merchant-invite-v1\0" || tenant_id || merchant_id || invitation_id)`. Der Delivery Key ist separat. Rotation: neuen Key hinzufügen und current setzen, alte Jobs beenden lassen, danach alten Key entfernen; ein verfrühtes Entfernen verhindert den Start. Ein kompromittierter Key plus bekannte Invitation IDs betrifft nur damit abgeleitete Einladungen bis expiry/revoke, nicht API- oder Finanzgeheimnisse.

Sperren/Löschen erzeugt ein dauerhaftes Signal; ein minimal berechtigter Worker widerruft Admin-Sessions und bestätigt atomar. Versionierte Einstellungen umfassen Name, Locale `en/zh-CN/es/fr/de/ru`, IANA-Zeitzone, optionale Support-E-Mail, Benachrichtigungen und höchstens 100 exakte HTTPS-Origins. HTTP, Wildcards, Credentials, Path, Query und Fragment sind verboten. Unbekanntes/doppeltes JSON, Secrets und Finanzrichtlinien werden abgewiesen. Jede Änderung benötigt Version und Begründung und erzeugt unveränderlichen Snapshot plus SHA-256-Auditkette.

Private API TLS 1.3 `:8447`, Health `:9095`; Session-Revocation `:9096`; Delivery `:9097`. E-Mail ist fail-closed. Assertions, Invite Tokens, Bearer, Key Ring, OIDC Tokens und vollständige Providerantworten nie loggen. Vertrag: `contracts/merchant-settings-openapi.yaml`; Mutationen benötigen `Idempotency-Key`.
