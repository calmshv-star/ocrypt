BEGIN;

DROP TRIGGER IF EXISTS payment_route_bind_hosted_link_job ON payment_routes;
DROP FUNCTION IF EXISTS bind_hosted_payment_link_job();
DROP TRIGGER IF EXISTS hosted_provider_create_attempt_request_immutable ON hosted_provider_create_attempts;
DROP FUNCTION IF EXISTS hosted_provider_create_attempt_reject_request_mutation();
DROP TRIGGER IF EXISTS hosted_payment_link_job_economic_immutable ON hosted_payment_link_jobs;
DROP FUNCTION IF EXISTS hosted_payment_link_job_reject_economic_mutation();
DROP TABLE IF EXISTS hosted_payment_link_incidents;
DROP TABLE IF EXISTS hosted_payment_link_jobs;
ALTER TABLE hosted_provider_create_attempts DROP CONSTRAINT IF EXISTS hosted_provider_create_attempts_job_unique;
ALTER TABLE payment_link_redemptions DROP CONSTRAINT IF EXISTS payment_link_redemptions_hosted_job_unique;

COMMIT;
