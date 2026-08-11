# Migration JSON-MD5/Form-MD5

L’adaptateur historique est un pont temporaire, désactivé par défaut. Il crée des intents/routes normaux dans le cœur, lit leur état via l’API adossée à PostgreSQL et n’émet le callback historique qu’après `payment.settled`. Il ne peut ni déclarer un paiement payé ni écrire dans le grand livre.

Appliquez la migration `000018_legacy_compatibility` avant toute demande d’admission.

Avant admission, créez une clé HMAC avec `payments:read`, `payments:write`, `events:read`, montez les secrets HMAC/MD5 sous forme de fichiers, imposez un callback HTTPS sur le port 443 et une correspondance unique devise/token/réseau. Deux identités opérateur distinctes demandent puis approuvent l’admission valable 30 minutes.

La signature concatène les `key=value` non vides triés puis le secret; JSON-MD5 omet `signature`, Form-MD5 omet `sign` et `sign_type`. Protégez le `trade_id` de 128 bits. Le polling sert uniquement à la reprise. Le crédit métier doit être idempotent et l’accusé doit être exactement `ok` ou `success` en minuscules.

Migrez vers HMAC et les webhooks canoniques avant `Sunset`. Admission expirée, trou de séquence, secret absent ou échec TLS ferment la readiness. Aucun test live n’est revendiqué.
