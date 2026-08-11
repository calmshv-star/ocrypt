BEGIN;

CREATE OR REPLACE FUNCTION request_legacy_compat_config_admission(
  requested_request_id uuid,requested_config_id uuid,requested_credential_id uuid,requested_tenant uuid,requested_merchant uuid,
  requested_protocol text,requested_pid text,requested_currency text,requested_scale smallint,
  requested_chain text,requested_asset text,requested_token text,requested_network text,requested_payment_type text,
  requested_ip_allowlist cidr[],requested_legacy_secret_ref text,requested_callback_key_id text,
  requested_core_key_id text,requested_core_secret_ref text,requested_valid_from timestamptz,
  requested_valid_until timestamptz,requested_sunset timestamptz,requested_manifest jsonb,
  requested_manifest_sha256 bytea
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE canonical_manifest jsonb; authoritative_at timestamptz:=clock_timestamp();
BEGIN
  canonical_manifest:=jsonb_build_object('asset_id',requested_asset,'chain_id',requested_chain,
    'config_id',requested_config_id,'core_key_id',requested_core_key_id,'core_secret_ref',requested_core_secret_ref,
    'credential_id',requested_credential_id,'currency',requested_currency,'currency_scale',requested_scale,
    'ip_allowlist',to_jsonb(requested_ip_allowlist),'legacy_payment_type',requested_payment_type,
    'legacy_network',requested_network,'legacy_secret_ref',requested_legacy_secret_ref,'legacy_token',requested_token,
    'merchant_id',requested_merchant,'pid',requested_pid,'protocol',requested_protocol,'sunset_at',requested_sunset,
    'tenant_id',requested_tenant,'valid_from',requested_valid_from,'valid_until',requested_valid_until);
  IF NOT pg_has_role(session_user,'legacy_compat_admission_requester','member') OR
     requested_manifest<>canonical_manifest OR digest(convert_to(requested_manifest::text,'UTF8'),'sha256')<>requested_manifest_sha256 OR
     requested_protocol NOT IN ('json_md5','form_md5') OR cardinality(requested_ip_allowlist) NOT BETWEEN 1 AND 64 OR
     requested_pid<>btrim(requested_pid) OR requested_currency<>upper(requested_currency) OR
     requested_token<>upper(requested_token) OR requested_network<>lower(requested_network) OR
     (requested_protocol='json_md5' AND requested_payment_type<>'json_md5') OR
     requested_sunset<=authoritative_at OR requested_valid_from>authoritative_at OR requested_valid_until<=authoritative_at OR requested_valid_until>requested_sunset THEN RETURN false; END IF;
  INSERT INTO public.legacy_compat_admission_requests(id,config_id,credential_id,tenant_id,merchant_id,protocol,pid,currency,
    currency_scale,chain_id,asset_id,legacy_token,legacy_network,legacy_payment_type,ip_allowlist,legacy_secret_ref,callback_key_id,
    core_key_id,core_secret_ref,valid_from,valid_until,sunset_at,manifest,manifest_sha256,requested_by,requested_at,expires_at,status)
  VALUES(requested_request_id,requested_config_id,requested_credential_id,requested_tenant,requested_merchant,requested_protocol,
    requested_pid,requested_currency,requested_scale,requested_chain,requested_asset,requested_token,
    requested_network,requested_payment_type,requested_ip_allowlist,requested_legacy_secret_ref,requested_callback_key_id,
    requested_core_key_id,requested_core_secret_ref,requested_valid_from,requested_valid_until,requested_sunset,requested_manifest,
    requested_manifest_sha256,session_user,authoritative_at,authoritative_at+interval '30 minutes','pending');
  RETURN true;
END $$;

COMMIT;
