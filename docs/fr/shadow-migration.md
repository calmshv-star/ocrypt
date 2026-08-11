# Procédure de migration miroir

La migration `000021` conserve PostgreSQL comme autorité comptable. `migration-control` vérifie hors ligne et uniquement en dry-run. L’inventaire borné sans secret doit être signé sur ses octets canoniques exacts par deux clés Ed25519 autorisées distinctes, puis soumis via l’API admin limitée au tenant.

Les transitions suivent inventaire, validation, demande/approbation/exécution distinctes, import, shadow et canari. La bascule reste en attente jusqu’à l’ACK de la version d’action et du fence par l’actionneur authentifié séparément. L’abandon du canari et le rollback conservent les faits et clôtures de propriété; les adresses watch-only importées ne sont jamais libérées.

Le Job démarre avec `MIGRATION_EXECUTE=false`; toute écriture exige un rôle dédié, lease/fence, mTLS 1.3 et des faits signés par quorum. La mise hors service exige zéro backlog dérivé de la base et les preuves d’archive, restauration et révocation de clé.

Aucune base source, chaîne, bascule PostgreSQL, actionneur, grappe Helm ou quorum fournisseur réel n’a été testé localement; joindre ces preuves au manifeste de livraison.
