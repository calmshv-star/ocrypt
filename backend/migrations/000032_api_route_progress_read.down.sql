BEGIN;

REVOKE SELECT ON payment_match_aggregates FROM merchant_api_runtime;

COMMIT;
