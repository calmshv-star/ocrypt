-- Sanctioned crypto addresses (TZ: "проверка адресов по открытым спискам,
-- предупреждение мерчанту о входящем платеже с санкционного адреса").
--
-- The screening importer loads the "Digital Currency Address" entries of an
-- official list together with its names (same source, same transaction).
-- Every new transfer's sender is checked when it is recorded; a hit is an
-- alert for each merchant whose payment address received it, and a
-- notification to everyone who gets that merchant's notifications. The
-- payment itself is not changed: the platform holds no funds, the merchant
-- decides. A failing check only logs a warning and never blocks the scanner.
--
-- A sanctioned address cannot be a go-live or deposit receiving address
-- (SQLSTATE OCP04, shown to the merchant as "address not accepted").
BEGIN;

CREATE TABLE sanctioned_addresses (
  source text NOT NULL REFERENCES signup_screening_sources(source) ON DELETE CASCADE,
  family text NOT NULL CHECK (family IN ('evm','tron','solana')),
  -- EVM: 0x and 40 lowercase hex digits; TRON: base58check; Solana: base58.
  address_key text NOT NULL,
  -- The 20-byte account as 40 lowercase hex digits (EVM and TRON), so a
  -- sender reported in hex form matches too.
  address_hex text,
  -- OFAC lists one account under several tags (ETH and USDT, say).
  currencies text[] NOT NULL CHECK (cardinality(currencies) BETWEEN 1 AND 20),
  reference text NOT NULL CHECK (length(reference) BETWEEN 1 AND 128),
  PRIMARY KEY (source,family,address_key,reference),
  CHECK ((family='evm' AND address_key ~ '^0x[0-9a-f]{40}$' AND address_hex=substr(address_key,3))
      OR (family='tron' AND address_key ~ '^T[1-9A-HJ-NP-Za-km-z]{33}$' AND address_hex ~ '^[0-9a-f]{40}$')
      OR (family='solana' AND address_key ~ '^[1-9A-HJ-NP-Za-km-z]{32,44}$' AND address_hex IS NULL))
);
CREATE INDEX sanctioned_addresses_key_idx ON sanctioned_addresses (family,address_key);
CREATE INDEX sanctioned_addresses_hex_idx ON sanctioned_addresses (family,address_hex) WHERE address_hex IS NOT NULL;

CREATE TABLE sanctioned_transfer_alerts (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  merchant_id uuid NOT NULL,
  event_id uuid NOT NULL REFERENCES transfer_events(id) ON DELETE CASCADE,
  chain_id text NOT NULL,
  transaction_id text NOT NULL,
  asset_id text NOT NULL,
  from_address text NOT NULL,
  to_address text NOT NULL,
  amount_atomic uint256 NOT NULL,
  asset_decimals smallint NOT NULL CHECK (asset_decimals BETWEEN 0 AND 77),
  matches jsonb NOT NULL CHECK (jsonb_typeof(matches)='array' AND jsonb_array_length(matches) BETWEEN 1 AND 20),
  detected_at timestamptz NOT NULL,
  FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE CASCADE,
  UNIQUE (event_id,merchant_id)
);
CREATE INDEX sanctioned_transfer_alerts_merchant_idx ON sanctioned_transfer_alerts (tenant_id,merchant_id,detected_at DESC,id);

ALTER TABLE sanctioned_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE sanctioned_transfer_alerts ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON sanctioned_addresses,sanctioned_transfer_alerts FROM PUBLIC;

ALTER TABLE notification_jobs DROP CONSTRAINT notification_jobs_event_check;
ALTER TABLE notification_jobs ADD CONSTRAINT notification_jobs_event_check
  CHECK (event IN ('paid','underpaid','review','webhook_failed','sign_in','sanctioned'));

-- Replaces the addresses of one source; the source row must have been
-- (re)loaded by replace_signup_screening_source in the same transaction.
-- Like the names, an official list (one with stale_after) may not be
-- emptied or shrunk below half, so a changed remark format or a truncated
-- download cannot silently drop the addresses. Owner only: the importer runs
-- as the database owner.
CREATE FUNCTION replace_sanctioned_addresses(p_source text,p_entries jsonb) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE loaded integer; previous integer; official boolean;
BEGIN
  SELECT stale_after IS NOT NULL INTO official FROM public.signup_screening_sources WHERE source=p_source;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'load the screening source % first',p_source USING ERRCODE='OCP02';
  END IF;
  IF p_entries IS NULL OR jsonb_typeof(p_entries)<>'array' THEN
    RAISE EXCEPTION 'address entries must be a JSON array' USING ERRCODE='OCP02';
  END IF;
  SELECT count(*) INTO previous FROM public.sanctioned_addresses WHERE source=p_source;
  DELETE FROM public.sanctioned_addresses WHERE source=p_source;
  INSERT INTO public.sanctioned_addresses(source,family,address_key,address_hex,currencies,reference)
  SELECT p_source,x.family,x.address_key,min(x.address_hex),array_agg(DISTINCT x.currency ORDER BY x.currency),x.reference
    FROM (SELECT y.family,CASE WHEN y.family='evm' THEN lower(y.address) ELSE y.address END AS address_key,
                 CASE WHEN y.family='evm' THEN substr(lower(y.address),3) WHEN y.family='tron' THEN lower(y.hex) END AS address_hex,
                 upper(y.currency) AS currency,y.reference
            FROM jsonb_to_recordset(p_entries) AS y(family text,address text,hex text,currency text,reference text)) x
   WHERE x.currency ~ '^[A-Z0-9]{2,10}$'
   GROUP BY x.family,x.address_key,x.reference;
  GET DIAGNOSTICS loaded=ROW_COUNT;
  IF official AND previous>0 AND loaded*2<previous THEN
    RAISE EXCEPTION 'refusing to replace % addresses of % with %',previous,p_source,loaded USING ERRCODE='55000';
  END IF;
  RETURN loaded;
END $$;

-- The list entries a sender address matches on its chain's family. A hex
-- sender (0x…, 41…) matches EVM and TRON accounts by their 20 bytes.
CREATE FUNCTION sanctioned_address_matches(p_chain text,p_address text) RETURNS TABLE(source text,reference text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  WITH sender AS (
    SELECT c.family,
           CASE WHEN p_address ~ '^(0x|41)?[0-9a-fA-F]{40}$' THEN lower(right(p_address,40)) END AS hex
      FROM public.chains c WHERE c.id=p_chain
  )
  SELECT a.source,a.reference FROM sender s JOIN public.sanctioned_addresses a ON a.family=s.family
   WHERE (s.family='evm' AND a.address_key=lower(p_address))
      OR (s.family IN ('evm','tron') AND s.hex IS NOT NULL AND a.address_hex=s.hex)
      OR (s.family IN ('tron','solana') AND a.address_key=p_address)
$$;

-- Everyone who receives this merchant's notifications (saved preferences or
-- the owner defaults) gets a sanctions alert whatever events they chose:
-- by email, and by Telegram when linked. The dedupe key (merchant, sender,
-- UTC hour) keeps dust from a listed address from flooding the inbox; every
-- alert is still recorded.
CREATE FUNCTION enqueue_sanctioned_transfer_notification(p_tenant uuid,p_merchant uuid,p_dedupe text,p_payload jsonb) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE inserted integer; now_at timestamptz:=clock_timestamp();
BEGIN
  WITH recipients AS (
    SELECT p.user_id,p.locale FROM public.notification_preferences p WHERE p.tenant_id=p_tenant AND p.merchant_id=p_merchant
    UNION ALL
    SELECT o.user_id,coalesce(a.locale,'en') FROM public.notification_default_owners(p_tenant,p_merchant) o
      LEFT JOIN public.notification_account_settings a ON a.user_id=o.user_id
  ), wanted AS (
    SELECT DISTINCT ON (w.user_id,w.channel) w.user_id,w.channel,w.locale FROM (
      SELECT r.user_id,'email'::text AS channel,r.locale FROM recipients r
      UNION ALL
      SELECT r.user_id,'telegram'::text,r.locale FROM recipients r JOIN public.telegram_chat_links l ON l.user_id=r.user_id
    ) w ORDER BY w.user_id,w.channel,w.locale
  )
  INSERT INTO public.notification_jobs(id,user_id,tenant_id,merchant_id,channel,event,dedupe_key,payload,locale,status,available_at,created_at,updated_at)
  SELECT gen_random_uuid(),w.user_id,p_tenant,p_merchant,w.channel,'sanctioned',p_dedupe,p_payload,w.locale,'pending',now_at,now_at,now_at
    FROM wanted w WHERE public.notification_recipient_allowed(w.user_id,p_tenant,p_merchant,'payments:read')
  ON CONFLICT (user_id,channel,dedupe_key) DO NOTHING;
  GET DIAGNOSTICS inserted=ROW_COUNT;
  RETURN inserted;
END $$;

-- The merchants a transfer was meant for: routes on that address for that
-- asset whose window covers the transfer (as matching uses them), else the
-- merchant that owns the deposit address. A tenant-shared address without a
-- covering route alerts nobody: no merchant can be told another's payment.
CREATE FUNCTION screen_transfer_sender() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE hits jsonb; route record; alert uuid; merchant_name text; shown jsonb;
BEGIN
  BEGIN
    SELECT jsonb_agg(DISTINCT jsonb_build_object('source',m.source,'reference',m.reference)) INTO hits
      FROM public.sanctioned_address_matches(NEW.chain_id,NEW.from_address) m;
    IF hits IS NOT NULL THEN
      IF jsonb_array_length(hits)>20 THEN
        hits := (SELECT jsonb_agg(h) FROM (SELECT h FROM jsonb_array_elements(hits) h LIMIT 20) x);
      END IF;
      -- Operator case references stay internal; merchants see the list.
      shown := (SELECT jsonb_agg(CASE WHEN h->>'source'='operator' THEN jsonb_build_object('source','operator','reference','') ELSE h END) FROM jsonb_array_elements(hits) h);
      FOR route IN
        SELECT DISTINCT r.tenant_id,r.merchant_id FROM public.payment_routes r
         WHERE r.chain_id=NEW.chain_id AND r.receiving_address=NEW.to_address AND r.asset_id=NEW.asset_id
           AND NEW.on_chain_time>=r.starts_at AND NEW.on_chain_time<r.grace_ends_at
        UNION
        SELECT w.tenant_id,w.merchant_id FROM public.addresses a JOIN public.wallets w ON w.id=a.wallet_id
         WHERE a.chain_id=NEW.chain_id AND a.canonical_address=NEW.to_address AND a.purpose='deposit' AND w.merchant_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM public.payment_routes r2 WHERE r2.chain_id=NEW.chain_id AND r2.receiving_address=NEW.to_address AND r2.asset_id=NEW.asset_id
                             AND NEW.on_chain_time>=r2.starts_at AND NEW.on_chain_time<r2.grace_ends_at)
      LOOP
        alert := NULL;
        INSERT INTO public.sanctioned_transfer_alerts(id,tenant_id,merchant_id,event_id,chain_id,transaction_id,asset_id,from_address,to_address,amount_atomic,asset_decimals,matches,detected_at)
        VALUES(gen_random_uuid(),route.tenant_id,route.merchant_id,NEW.id,NEW.chain_id,NEW.transaction_id,NEW.asset_id,NEW.from_address,NEW.to_address,NEW.amount_atomic,NEW.asset_decimals,shown,clock_timestamp())
        ON CONFLICT (event_id,merchant_id) DO NOTHING RETURNING id INTO alert;
        IF alert IS NOT NULL THEN
          SELECT m.display_name INTO merchant_name FROM public.merchants m WHERE m.id=route.merchant_id AND m.tenant_id=route.tenant_id;
          PERFORM public.enqueue_sanctioned_transfer_notification(route.tenant_id,route.merchant_id,
            'sanctioned:'||route.merchant_id::text||':'||lower(left(NEW.from_address,128))||':'||to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYYMMDDHH24'),
            jsonb_strip_nulls(jsonb_build_object('merchant',left(merchant_name,200),'network',left(NEW.chain_id,64),'sender',left(NEW.from_address,128),
              'transaction',left(NEW.transaction_id,128),'amount_atomic',NEW.amount_atomic::text,'asset_decimals',NEW.asset_decimals,
              'asset',left(NEW.asset_id,128),'reference',left(nullif(shown->0->>'reference',''),128))));
        END IF;
      END LOOP;
    END IF;
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'sanctions screening skipped for transfer % (%)',NEW.id,SQLSTATE;
  END;
  RETURN NEW;
END $$;
CREATE TRIGGER transfer_events_screen_sender AFTER INSERT ON transfer_events FOR EACH ROW EXECUTE FUNCTION screen_transfer_sender();

-- A receiving address on a sanctions list can neither go live nor become a
-- deposit address later (a wallet's address replacement included).
CREATE FUNCTION refuse_sanctioned_receiving_address() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.sanctioned_address_matches(NEW.chain_id,NEW.canonical_address)) THEN
    RAISE EXCEPTION 'receiving address is not accepted' USING ERRCODE='OCP04';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER merchant_go_live_addresses_screen BEFORE INSERT ON merchant_go_live_addresses FOR EACH ROW EXECUTE FUNCTION refuse_sanctioned_receiving_address();
CREATE TRIGGER addresses_screen_deposit BEFORE INSERT OR UPDATE OF canonical_address,purpose ON addresses FOR EACH ROW
  WHEN (NEW.purpose='deposit') EXECUTE FUNCTION refuse_sanctioned_receiving_address();

-- Align the notification access check with the cabinet's: a global binding
-- that carries the permission counts too (it already opens the pages).
CREATE OR REPLACE FUNCTION notification_recipient_allowed(p_user uuid,p_tenant uuid,p_merchant uuid,p_permission text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.admin_users u
      JOIN public.tenants t ON t.id=p_tenant AND t.status='active'
      JOIN public.merchants m ON m.id=p_merchant AND m.tenant_id=p_tenant AND m.status='active'
     WHERE u.id=p_user AND u.status='active' AND p_permission IN ('payments:read','webhooks:read') AND (
       EXISTS (SELECT 1 FROM public.admin_role_bindings b
                 JOIN public.admin_role_permissions rp ON rp.role_key=b.role_key AND rp.permission_key=p_permission
                WHERE b.user_id=u.id AND b.revoked_at IS NULL AND (b.expires_at IS NULL OR b.expires_at>now())
                  AND (b.tenant_id IS NULL OR (b.tenant_id=p_tenant AND (b.merchant_id IS NULL OR b.merchant_id=p_merchant))))
       OR EXISTS (SELECT 1 FROM public.notification_default_owners(p_tenant,p_merchant) o
                    JOIN public.admin_role_permissions rp ON rp.role_key='merchant_owner' AND rp.permission_key=p_permission
                   WHERE o.user_id=u.id)))
$$;

-- The merchant's alerts, newest first, for a user who reads its payments.
CREATE FUNCTION list_sanctioned_transfer_alerts(p_tenant uuid,p_merchant uuid,p_limit integer)
RETURNS TABLE(id uuid,chain_id text,transaction_id text,asset_id text,from_address text,to_address text,amount_atomic text,asset_decimals smallint,matches jsonb,detected_at timestamptz)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid:=nullif(current_setting('app.admin_user_id',true),'')::uuid;
BEGIN
  IF actor IS NULL OR p_merchant IS NULL OR NOT public.notification_recipient_allowed(actor,p_tenant,p_merchant,'payments:read') THEN
    RAISE EXCEPTION 'sanctions alerts need payments access' USING ERRCODE='OCP01';
  END IF;
  RETURN QUERY SELECT a.id,a.chain_id,a.transaction_id,a.asset_id,a.from_address,a.to_address,a.amount_atomic::text,a.asset_decimals,a.matches,a.detected_at
    FROM public.sanctioned_transfer_alerts a
   WHERE a.tenant_id=p_tenant AND a.merchant_id=p_merchant
   ORDER BY a.detected_at DESC,a.id LIMIT least(greatest(coalesce(p_limit,100),1),200);
END $$;

REVOKE ALL ON FUNCTION replace_sanctioned_addresses(text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION sanctioned_address_matches(text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION enqueue_sanctioned_transfer_notification(uuid,uuid,text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION screen_transfer_sender() FROM PUBLIC;
REVOKE ALL ON FUNCTION refuse_sanctioned_receiving_address() FROM PUBLIC;
REVOKE ALL ON FUNCTION list_sanctioned_transfer_alerts(uuid,uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION list_sanctioned_transfer_alerts(uuid,uuid,integer) TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
