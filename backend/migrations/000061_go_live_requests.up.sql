BEGIN;

-- REG-4: a self-service merchant proves its receiving addresses, chooses an
-- acceptance preset and asks to go live. A platform operator admits the
-- wallets with the existing four-eyes platform tools and then approves the
-- request, which activates the preset's policy (owner requested, operator
-- approved: two people) and switches the merchant to live in one transaction.

INSERT INTO admin_permissions(permission_key,description) VALUES
  ('go_live:request','Ask a platform operator to switch the merchant to live payments'),
  ('go_live_reviews:read','Read merchant go-live requests'),
  ('go_live_reviews:decide','Approve or reject a merchant go-live request');
INSERT INTO admin_role_permissions(role_key,permission_key) VALUES
  ('merchant_owner','go_live:request'),
  ('security_admin','go_live_reviews:read'),('security_admin','go_live_reviews:decide'),
  ('auditor','go_live_reviews:read');

-- Matching-policy values of each preset. Strict files no policy: without one
-- only an exact payment settles and everything else goes to review.
CREATE TABLE acceptance_presets (
  preset text PRIMARY KEY CHECK (preset IN ('strict','standard','fast')),
  files_policy boolean NOT NULL,
  accumulate_partials boolean NOT NULL,
  underpayment_tolerance_bps integer NOT NULL CHECK (underpayment_tolerance_bps BETWEEN 0 AND 10000),
  overpayment_mode text NOT NULL CHECK (overpayment_mode IN ('manual_review','credit_all','credit_expected_hold_excess')),
  accept_late_within_grace boolean NOT NULL,
  require_same_sender boolean NOT NULL
);
INSERT INTO acceptance_presets VALUES
  ('strict',false,false,0,'manual_review',false,true),
  ('standard',true,true,50,'credit_expected_hold_excess',true,true),
  ('fast',true,true,200,'credit_expected_hold_excess',true,false);
REVOKE ALL ON acceptance_presets FROM PUBLIC;

CREATE TABLE merchant_go_live_requests (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  merchant_id uuid NOT NULL,
  requested_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
  preset text NOT NULL REFERENCES acceptance_presets(preset) ON DELETE RESTRICT,
  policy_change_id uuid,
  status text NOT NULL CHECK (status IN ('pending','approved','rejected')),
  created_at timestamptz NOT NULL,
  decided_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
  decided_at timestamptz,
  decision_reason text CHECK (decision_reason IS NULL OR length(decision_reason) BETWEEN 8 AND 1000),
  FOREIGN KEY (merchant_id,tenant_id) REFERENCES merchants(id,tenant_id) ON DELETE RESTRICT,
  FOREIGN KEY (policy_change_id,tenant_id) REFERENCES automated_matching_policy_changes(id,tenant_id) ON DELETE RESTRICT,
  CHECK ((status='pending')=(decided_by IS NULL AND decided_at IS NULL AND decision_reason IS NULL)),
  CHECK (decided_by IS NULL OR decided_by<>requested_by)
);
CREATE UNIQUE INDEX merchant_go_live_one_open_idx ON merchant_go_live_requests(merchant_id) WHERE status='pending';
CREATE INDEX merchant_go_live_pending_idx ON merchant_go_live_requests(created_at) WHERE status='pending';

-- Each address carries the signed proof the cabinet verified, so an operator
-- (and an auditor later) can re-check who controls it.
CREATE TABLE merchant_go_live_addresses (
  request_id uuid NOT NULL REFERENCES merchant_go_live_requests(id) ON DELETE CASCADE,
  chain_id text NOT NULL REFERENCES chains(id) ON DELETE RESTRICT,
  canonical_address text NOT NULL CHECK (length(canonical_address) BETWEEN 8 AND 256),
  display_address text NOT NULL CHECK (length(display_address) BETWEEN 8 AND 256),
  proof_kind text NOT NULL CHECK (proof_kind IN ('evm_personal_sign','solana_sign_message','tron_sign_message_v2')),
  proof_nonce uuid NOT NULL UNIQUE,
  proof_message text NOT NULL CHECK (length(proof_message) BETWEEN 1 AND 4000),
  proof_signature text NOT NULL CHECK (length(proof_signature) BETWEEN 1 AND 512),
  proven_at timestamptz NOT NULL,
  PRIMARY KEY (request_id,chain_id)
);
REVOKE ALL ON merchant_go_live_requests,merchant_go_live_addresses FROM PUBLIC;

-- True when the current admin user is an active owner of the active merchant
-- (the same facts list_current_admin_self_service_owner_grants projects).
CREATE FUNCTION current_admin_owns_merchant(p_tenant uuid,p_merchant uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.admin_users u
      JOIN public.merchant_members m ON m.admin_user_id=u.id AND m.status='active'
       AND m.oidc_issuer=u.oidc_issuer AND m.oidc_subject=u.oidc_subject AND lower(m.email)=lower(u.email)
      JOIN public.merchant_member_role_bindings b ON b.member_id=m.id AND b.tenant_id=m.tenant_id
       AND b.merchant_id=m.merchant_id AND b.role_key='owner' AND b.revoked_at IS NULL
      JOIN public.merchants merchant ON merchant.id=m.merchant_id AND merchant.tenant_id=m.tenant_id AND merchant.status='active'
      JOIN public.tenants t ON t.id=m.tenant_id AND t.status='active'
     WHERE u.id=nullif(current_setting('app.admin_user_id',true),'')::uuid AND u.status='active'
       AND m.tenant_id=p_tenant AND m.merchant_id=p_merchant)
$$;
REVOKE ALL ON FUNCTION current_admin_owns_merchant(uuid,uuid) FROM PUBLIC;

-- Files a go-live request for the owner's own test-mode merchant. Errors use
-- OCP01 (not the owner), OCP02 (invalid input) and 55000 (not possible now).
CREATE FUNCTION submit_merchant_go_live_request(
  p_request uuid,p_tenant uuid,p_merchant uuid,p_preset text,p_addresses jsonb,p_policy_change uuid,p_at timestamptz
) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid := nullif(current_setting('app.admin_user_id',true),'')::uuid;
  target public.merchants%ROWTYPE; preset public.acceptance_presets%ROWTYPE; next_version bigint; change_id uuid;
BEGIN
  IF NOT public.current_admin_owns_merchant(p_tenant,p_merchant) THEN
    RAISE EXCEPTION 'only an active owner of the merchant can request live mode' USING ERRCODE='OCP01';
  END IF;
  SELECT * INTO preset FROM public.acceptance_presets WHERE acceptance_presets.preset=p_preset;
  IF NOT FOUND OR p_addresses IS NULL OR jsonb_typeof(p_addresses)<>'array'
     OR jsonb_array_length(p_addresses) NOT BETWEEN 1 AND 16
     OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_addresses) a
                 WHERE jsonb_typeof(a)<>'object' OR NOT EXISTS (SELECT 1 FROM public.chains c WHERE c.id=a->>'chain_id' AND c.status='active')
                    OR coalesce(a->>'proof_kind','') NOT IN ('evm_personal_sign','solana_sign_message','tron_sign_message_v2'))
     OR (SELECT count(DISTINCT a->>'chain_id') FROM jsonb_array_elements(p_addresses) a)<>jsonb_array_length(p_addresses)
     OR (preset.files_policy AND p_policy_change IS NULL) THEN
    RAISE EXCEPTION 'go-live request is invalid' USING ERRCODE='OCP02';
  END IF;
  SELECT * INTO target FROM public.merchants m WHERE m.id=p_merchant AND m.tenant_id=p_tenant FOR UPDATE;
  IF target.environment<>'test' THEN
    RAISE EXCEPTION 'merchant is already live' USING ERRCODE='55000';
  END IF;
  IF EXISTS (SELECT 1 FROM public.merchant_signup_screenings s WHERE s.merchant_id=p_merchant AND s.status IN ('review','rejected')) THEN
    RAISE EXCEPTION 'signup screening is not complete' USING ERRCODE='55000';
  END IF;
  IF EXISTS (SELECT 1 FROM public.merchant_go_live_requests r WHERE r.merchant_id=p_merchant AND r.status='pending') THEN
    RAISE EXCEPTION 'a go-live request is already open' USING ERRCODE='55000';
  END IF;
  IF preset.files_policy THEN
    SELECT greatest(coalesce((SELECT max(p.version) FROM public.automated_matching_policies p WHERE p.tenant_id=p_tenant AND p.merchant_id=p_merchant),0),
                    coalesce((SELECT max(c.proposed_version) FROM public.automated_matching_policy_changes c WHERE c.tenant_id=p_tenant AND c.merchant_id=p_merchant),0))+1
      INTO next_version;
    INSERT INTO public.automated_matching_policy_changes(id,tenant_id,merchant_id,proposed_version,accumulate_partials,underpayment_tolerance_bps,
        overpayment_mode,accept_late_within_grace,require_same_sender,gasfree_enabled,gasfree_fee_collectors,status,created_by,requested_by,
        request_reason,created_at,updated_at,version)
      VALUES(p_policy_change,p_tenant,p_merchant,next_version,preset.accumulate_partials,preset.underpayment_tolerance_bps,
        preset.overpayment_mode,preset.accept_late_within_grace,preset.require_same_sender,false,'{}',
        'pending_approval',actor,actor,'Self-service go-live preset '||preset.preset,p_at,p_at,1);
    change_id := p_policy_change;
  END IF;
  INSERT INTO public.merchant_go_live_requests(id,tenant_id,merchant_id,requested_by,preset,policy_change_id,status,created_at)
    VALUES(p_request,p_tenant,p_merchant,actor,preset.preset,change_id,'pending',p_at);
  INSERT INTO public.merchant_go_live_addresses(request_id,chain_id,canonical_address,display_address,proof_kind,proof_nonce,proof_message,proof_signature,proven_at)
    SELECT p_request,a->>'chain_id',a->>'canonical_address',a->>'display_address',a->>'proof_kind',(a->>'proof_nonce')::uuid,
           a->>'proof_message',a->>'proof_signature',(a->>'proven_at')::timestamptz
      FROM jsonb_array_elements(p_addresses) a;
  PERFORM public.append_admin_audit(gen_random_uuid(),p_tenant,p_merchant,actor,NULL,
    'admin.go_live.requested','merchant',p_merchant::text,p_request::text,'self-service go-live request',NULL,NULL,
    jsonb_build_object('preset',preset.preset,'chains',(SELECT jsonb_agg(a->>'chain_id' ORDER BY a->>'chain_id') FROM jsonb_array_elements(p_addresses) a)),NULL,NULL,p_at);
  RETURN p_request;
END $$;
REVOKE ALL ON FUNCTION submit_merchant_go_live_request(uuid,uuid,uuid,text,jsonb,uuid,timestamptz) FROM PUBLIC;

-- What the owner sees: the environment and the latest request's state and
-- preset, never the operator's reason.
CREATE FUNCTION merchant_go_live_state(p_tenant uuid,p_merchant uuid)
RETURNS TABLE(environment text,request_id uuid,request_status text,preset text,requested_at timestamptz,screening_ready boolean)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF NOT public.current_admin_owns_merchant(p_tenant,p_merchant) THEN
    RAISE EXCEPTION 'only an active owner of the merchant can read its go-live state' USING ERRCODE='OCP01';
  END IF;
  RETURN QUERY
    SELECT m.environment::text,r.id,r.status,r.preset,r.created_at,
           NOT EXISTS (SELECT 1 FROM public.merchant_signup_screenings s WHERE s.merchant_id=m.id AND s.status IN ('review','rejected'))
      FROM public.merchants m
      LEFT JOIN LATERAL (SELECT * FROM public.merchant_go_live_requests x WHERE x.merchant_id=m.id ORDER BY x.created_at DESC LIMIT 1) r ON true
     WHERE m.id=p_merchant AND m.tenant_id=p_tenant;
END $$;
REVOKE ALL ON FUNCTION merchant_go_live_state(uuid,uuid) FROM PUBLIC;

-- True when the address is the current deposit address of an admitted
-- watch-only wallet of the merchant on that chain.
CREATE FUNCTION go_live_address_admitted(p_tenant uuid,p_merchant uuid,p_chain text,p_canonical text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
  SELECT EXISTS (
    SELECT 1 FROM public.wallets w
      JOIN public.addresses a ON a.tenant_id=w.tenant_id AND a.wallet_id=w.id AND a.purpose='deposit' AND a.status IN ('available','assigned')
     WHERE w.tenant_id=p_tenant AND w.merchant_id=p_merchant AND w.chain_id=p_chain AND w.status='active'
       AND w.custody_mode='watch_only' AND w.signer_key_reference IS NULL AND a.canonical_address=p_canonical
       AND EXISTS (SELECT 1 FROM public.platform_wallet_runtime_admission(w.tenant_id,w.id,w.chain_id)))
$$;
REVOKE ALL ON FUNCTION go_live_address_admitted(uuid,uuid,text,text) FROM PUBLIC;

-- Pending go-live requests, oldest first, for a global reviewer.
CREATE FUNCTION list_go_live_requests(p_limit integer)
RETURNS TABLE(request_id uuid,tenant_id uuid,merchant_id uuid,company_name text,owner_email text,preset text,
  screening_status text,addresses jsonb,requested_at timestamptz)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
BEGIN
  IF NOT public.current_admin_has_global_permission('go_live_reviews:read') THEN
    RAISE EXCEPTION 'go-live review requires a global go_live_reviews:read binding' USING ERRCODE='OCP01';
  END IF;
  RETURN QUERY
    SELECT r.id,r.tenant_id,r.merchant_id,m.display_name,coalesce(u.email,''),r.preset,
           coalesce((SELECT s.status FROM public.merchant_signup_screenings s WHERE s.merchant_id=r.merchant_id),'operator_managed'),
           coalesce((SELECT jsonb_agg(jsonb_build_object('chain_id',a.chain_id,'address',a.display_address,'proof_kind',a.proof_kind,
                       'admitted',public.go_live_address_admitted(r.tenant_id,r.merchant_id,a.chain_id,a.canonical_address)) ORDER BY a.chain_id)
                       FROM public.merchant_go_live_addresses a WHERE a.request_id=r.id),'[]'::jsonb),
           r.created_at
      FROM public.merchant_go_live_requests r
      JOIN public.merchants m ON m.id=r.merchant_id AND m.tenant_id=r.tenant_id
      JOIN public.admin_users u ON u.id=r.requested_by
     WHERE r.status='pending'
     ORDER BY r.created_at,r.id
     LIMIT least(greatest(coalesce(p_limit,100),1),200);
END $$;
REVOKE ALL ON FUNCTION list_go_live_requests(integer) FROM PUBLIC;

-- Approves or rejects one request, once. Approval requires every requested
-- address to be admitted, activates the preset's policy with the operator as
-- approver and activator (never the requester) and switches the merchant to
-- live. The decision and its reason go to the global audit log only.
CREATE FUNCTION decide_go_live_request(p_request uuid,p_decision text,p_reason text,p_at timestamptz)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,public SET row_security=off AS $$
DECLARE actor uuid := nullif(current_setting('app.admin_user_id',true),'')::uuid;
  request public.merchant_go_live_requests%ROWTYPE; change public.automated_matching_policy_changes%ROWTYPE;
  reason text := btrim(coalesce(p_reason,'')); outcome text; snapshot jsonb;
BEGIN
  IF NOT public.current_admin_has_global_permission('go_live_reviews:decide') THEN
    RAISE EXCEPTION 'go-live decision requires a global go_live_reviews:decide binding' USING ERRCODE='OCP01';
  END IF;
  IF p_decision NOT IN ('approve','reject') OR length(reason) NOT BETWEEN 8 AND 1000 THEN
    RAISE EXCEPTION 'decision must be approve or reject with a reason of 8 to 1000 characters' USING ERRCODE='OCP02';
  END IF;
  SELECT * INTO request FROM public.merchant_go_live_requests r WHERE r.id=p_request FOR UPDATE;
  IF NOT FOUND OR request.status<>'pending' THEN
    RAISE EXCEPTION 'go-live request is not pending' USING ERRCODE='55000';
  END IF;
  IF request.requested_by=actor OR EXISTS (SELECT 1 FROM public.merchant_members m WHERE m.merchant_id=request.merchant_id AND m.admin_user_id=actor) THEN
    RAISE EXCEPTION 'a merchant member cannot decide its own go-live request' USING ERRCODE='OCP01';
  END IF;
  outcome := CASE p_decision WHEN 'approve' THEN 'approved' ELSE 'rejected' END;
  IF request.policy_change_id IS NOT NULL THEN
    SELECT * INTO change FROM public.automated_matching_policy_changes c
     WHERE c.id=request.policy_change_id AND c.tenant_id=request.tenant_id FOR UPDATE;
    IF change.status<>'pending_approval' OR change.requested_by=actor THEN
      RAISE EXCEPTION 'the preset policy change is no longer pending' USING ERRCODE='55000';
    END IF;
  END IF;
  IF outcome='approved' THEN
    IF EXISTS (SELECT 1 FROM public.merchant_go_live_addresses a WHERE a.request_id=request.id
                AND NOT public.go_live_address_admitted(request.tenant_id,request.merchant_id,a.chain_id,a.canonical_address)) THEN
      RAISE EXCEPTION 'admit every requested receiving address before approving' USING ERRCODE='55000';
    END IF;
    IF request.policy_change_id IS NOT NULL THEN
      UPDATE public.automated_matching_policy_changes SET status='activated',approved_by=actor,approval_reason=reason,approved_at=p_at,
          activated_by=actor,activation_reason=reason,activated_at=p_at,effective_at=p_at,updated_at=p_at,version=version+1
       WHERE id=change.id AND tenant_id=change.tenant_id;
      snapshot := jsonb_build_object('accumulate_partials',change.accumulate_partials,'underpayment_tolerance_bps',change.underpayment_tolerance_bps,
        'overpayment_mode',change.overpayment_mode,'accept_late_within_grace',change.accept_late_within_grace,
        'require_same_sender',change.require_same_sender,'gasfree_enabled',change.gasfree_enabled,'gasfree_fee_collectors',to_jsonb(change.gasfree_fee_collectors));
      INSERT INTO public.automated_matching_policies(id,tenant_id,merchant_id,version,accumulate_partials,underpayment_tolerance_bps,overpayment_mode,
          accept_late_within_grace,require_same_sender,gasfree_enabled,gasfree_fee_collectors,effective_at,change_request_id,requested_by,approved_by,
          activated_by,approval_reference,config_hash,created_at)
        VALUES(gen_random_uuid(),change.tenant_id,change.merchant_id,change.proposed_version,change.accumulate_partials,change.underpayment_tolerance_bps,
          change.overpayment_mode,change.accept_late_within_grace,change.require_same_sender,change.gasfree_enabled,change.gasfree_fee_collectors,p_at,
          change.id,change.requested_by,actor,actor,reason,digest(convert_to(snapshot::text,'UTF8'),'sha256'),p_at);
    END IF;
    UPDATE public.merchants SET environment='live',updated_at=p_at,version=version+1
     WHERE id=request.merchant_id AND tenant_id=request.tenant_id AND environment='test' AND status='active';
    IF NOT FOUND THEN
      RAISE EXCEPTION 'merchant is not an active test-mode merchant' USING ERRCODE='55000';
    END IF;
  ELSIF request.policy_change_id IS NOT NULL THEN
    UPDATE public.automated_matching_policy_changes SET status='rejected',approved_by=actor,approval_reason=reason,approved_at=p_at,updated_at=p_at,version=version+1
     WHERE id=change.id AND tenant_id=change.tenant_id;
  END IF;
  UPDATE public.merchant_go_live_requests SET status=outcome,decided_by=actor,decided_at=p_at,decision_reason=reason WHERE id=request.id;
  PERFORM public.append_admin_audit(gen_random_uuid(),NULL,NULL,actor,NULL,
    'admin.go_live.'||outcome,'merchant',request.merchant_id::text,request.id::text,reason,NULL,NULL,
    jsonb_build_object('preset',request.preset,'tenant_id',request.tenant_id),NULL,NULL,p_at);
  RETURN outcome;
END $$;
REVOKE ALL ON FUNCTION decide_go_live_request(uuid,text,text,timestamptz) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='merchant_admin_runtime') THEN
    GRANT EXECUTE ON FUNCTION submit_merchant_go_live_request(uuid,uuid,uuid,text,jsonb,uuid,timestamptz),
      merchant_go_live_state(uuid,uuid),list_go_live_requests(integer),decide_go_live_request(uuid,text,text,timestamptz)
      TO merchant_admin_runtime;
  END IF;
END $$;

COMMIT;
