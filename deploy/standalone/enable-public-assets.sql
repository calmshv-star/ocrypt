\set ON_ERROR_STOP on

BEGIN;
\ir activate-platform-snapshot.sql
\ir bootstrap-public-assets.sql
COMMIT;

\ir bootstrap-rates.sql
