BEGIN;

DROP FUNCTION IF EXISTS retention_worker_health(timestamptz,integer);
DROP FUNCTION IF EXISTS retention_advance_prune(uuid,uuid,bigint,timestamptz);
DROP FUNCTION IF EXISTS retention_claim_prune(text,timestamptz,integer);
DROP FUNCTION IF EXISTS retention_prune_published_outbox_payload(uuid,timestamptz);
DROP FUNCTION IF EXISTS retention_prune_admitted(uuid,timestamptz);
DROP FUNCTION IF EXISTS retention_fail_archive(uuid,uuid,bigint,text,timestamptz);
DROP FUNCTION IF EXISTS retention_acknowledge_archive(uuid,uuid,bigint,text,text,bigint,bytea,bytea,text,bytea,text,timestamptz,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS retention_claim_archive_batch(text,timestamptz,integer,integer);
DROP FUNCTION IF EXISTS retention_source_candidate_exists(uuid,text,timestamptz);

DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM outbox_events WHERE payload_tombstone_version<>0) THEN
    RAISE EXCEPTION 'cannot roll back retention archive while outbox tombstones exist; restore verified payloads first';
  END IF;
END $$;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS outbox_payload_archive_batch_fk,
  DROP CONSTRAINT IF EXISTS outbox_payload_tombstone_shape,
  DROP COLUMN IF EXISTS payload_tombstone_version,DROP COLUMN IF EXISTS payload_pruned_at,
  DROP COLUMN IF EXISTS payload_original_digest,DROP COLUMN IF EXISTS payload_archive_batch_id;
DROP FUNCTION IF EXISTS release_retention_legal_hold(uuid,text,text,timestamptz);
DROP FUNCTION IF EXISTS create_retention_legal_hold(uuid,uuid,text,text,uuid,text,uuid,text,text,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS create_retention_policy_version(uuid,uuid,text,bigint,integer,integer,integer,boolean,timestamptz,text,timestamptz);
DROP TABLE IF EXISTS retention_archive_index;
DROP TABLE IF EXISTS retention_archive_objects;
DROP TABLE IF EXISTS retention_archive_batch_items;
DROP TABLE IF EXISTS retention_archive_batches;
DROP TABLE IF EXISTS retention_archive_jobs;
DROP TABLE IF EXISTS retention_legal_holds;
DROP TABLE IF EXISTS retention_policy_versions;
DROP FUNCTION IF EXISTS retention_guard_legal_hold_mutation();
DROP FUNCTION IF EXISTS retention_reject_immutable_mutation();

COMMIT;
