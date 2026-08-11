# Guides d'exploitation

Ces procédures s'appliquent à la production et à la préproduction. Lors d'un
incident, commencez par les contrôles communs du guide des incidents critiques,
puis suivez le scénario concerné. Aucun accès de restauration à la base de
production ne doit être accordé sans exercice préalable.

- [Incidents critiques](critical-incidents.md) : indisponibilité, divergence du
  scanner, réorganisation, callbacks, paiements non identifiés et fuite de secrets.
- [Sauvegarde et restauration](backup-restore.md) : contrôles, restauration isolée,
  rapprochement et bascule.
- La procédure canonique de release/rollback se trouve dans la section anglaise.

Le scanner est désactivé : son binaire et ses adaptateurs n'ont pas franchi la
release gate. Un Deployment présent ou un health check réussi n'autorise pas
l'ingestion. La production exige des digests signés, des Secrets externes, des rôles
DB distincts, des NetworkPolicies appliquées, TLS vers PostgreSQL, PITR et exercice
validés, une astreinte et des alertes testées. Les seuls health checks ne remplacent
pas les métriques financières et de file, encore absentes.
