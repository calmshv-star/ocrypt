BEGIN;

-- Matching policies and their approval evidence are append-only. Rolling back
-- the application must not delete or rewrite a policy already bound to routes;
-- operators can activate a later restrictive version through the control plane.

COMMIT;
