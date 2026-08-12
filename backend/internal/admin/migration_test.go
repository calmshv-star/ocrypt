package admin

import (
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"strings"
	"testing"
)

func TestAdminMigrationContainsSecurityInvariants(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000002_admin_control.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	required := []string{
		"CHECK (octet_length(session_hash) = 32)",
		"admin_audit_append_only", "pg_advisory_xact_lock", "previous_hash bytea", "entry_hash bytea NOT NULL UNIQUE",
		"manual_resolution_actor_guard", "core_resolution_id uuid NOT NULL UNIQUE REFERENCES manual_resolutions",
		"approved_by IS NULL OR approved_by <> requested_by",
		"ALTER TABLE admin_action_requests FORCE ROW LEVEL SECURITY", "app.admin_allow_tenant_wide",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing invariant %q", fragment)
		}
	}
	if strings.Contains(sql, "encrypted_signing_secret") || strings.Contains(sql, "encrypted_secret bytea") {
		t.Fatal("admin migration should not copy merchant secrets")
	}
}

func TestIdempotencySerializesFirstUseAndRetriesSerialization(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"pg_advisory_xact_lock(hashtextextended", "withinScopeWrite", "attempt < 3"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("idempotency concurrency control missing %q", fragment)
		}
	}
	if !retryableAdminTransaction(&pgconn.PgError{Code: "40001"}) || !retryableAdminTransaction(&pgconn.PgError{Code: "40P01"}) || retryableAdminTransaction(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("unexpected transaction retry classification")
	}
}

func TestAuditChainReadsGlobalPredecessorAndRestoresRLSContext(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000002_admin_control.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, fragment := range []string{"USING (coalesce(nullif(current_setting('app.admin_audit_global'", "SELECT entry_hash INTO prior FROM public.admin_audit_log ORDER BY chain_position DESC LIMIT 1", "prior_admin_audit_global text := current_setting", "set_config('app.admin_audit_global',coalesce(prior_admin_audit_global,''),true)"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("global audit chain isolation missing %q", fragment)
		}
	}
}

func TestAdminSQLUsesLatestMerchantScopedCandidates(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"pr.merchant_id=$1", "cardinality(mc.disqualifiers)=0", "mc.candidate_set_version=(SELECT max(latest.candidate_set_version)"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("merchant candidate fencing missing %q", fragment)
		}
	}
}

func TestOverviewWebhookHealthIsMerchantScopedAndReadable(t *testing.T) {
	repository, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"JOIN callback_events e ON e.id=d.callback_event_id", "e.merchant_id=$1"} {
		if !strings.Contains(string(repository), fragment) {
			t.Errorf("overview webhook health is not merchant scoped: missing %q", fragment)
		}
	}
	grants, err := os.ReadFile("../../../deploy/postgres/runtime-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	adminGrant := string(grants)
	start := strings.Index(adminGrant, "GRANT SELECT ON\n  admin_users")
	if start < 0 {
		t.Fatal("merchant admin runtime SELECT grant block is missing")
	}
	end := strings.Index(adminGrant[start:], "TO merchant_admin_runtime;")
	if end < 0 || !strings.Contains(adminGrant[start:start+end], "callback_events") {
		t.Fatal("merchant admin runtime cannot read the callback events required for scoped dashboard health")
	}
}

func TestTransferRowsExposeHumanAmountMetadata(t *testing.T) {
	repository, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(repository)
	for _, fragment := range []string{"JOIN assets a ON a.id=e.asset_id AND a.chain_id=e.chain_id", "a.symbol,a.decimals", "&v.AssetSymbol, &v.AssetDecimals, &v.AmountAtomic"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("transfer list is missing display amount metadata: %q", fragment)
		}
	}
}

func TestManualResolutionBridgePersistsAndRechecksCandidateVersion(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"candidate_set_version,idempotency_key", "Scan(&eventID, &candidateSetVersion)", "c.candidate_set_version=mr.candidate_set_version", "pr.merchant_id=$3 AND cardinality(c.disqualifiers)=0", "FOR UPDATE OF mr,u"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("manual-resolution fence missing %q", fragment)
		}
	}
}

func TestClaimMutationContainsMerchantFence(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (r *PostgresRepository) assignUnmatched")
	if start < 0 {
		t.Fatal("assignUnmatched not found")
	}
	tail := source[start:]
	end := strings.Index(tail, "func (r *PostgresRepository) CreateActionRequest")
	if end < 0 {
		t.Fatal("CreateActionRequest not found")
	}
	method := tail[:end]
	for _, fragment := range []string{"pr.merchant_id=$5::uuid", "cardinality(mc.disqualifiers)=0", "candidate_set_version=(SELECT max"} {
		if !strings.Contains(method, fragment) {
			t.Errorf("claim/release merchant fence missing %q", fragment)
		}
	}
}

func TestSessionCookieSecurityIsExplicitInHandler(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"Secure: true", "HttpOnly: true", "SameSite: http.SameSiteStrictMode", "__Host-admin_session"} {
		if !strings.Contains(source, fragment) {
			t.Errorf("cookie security missing %q", fragment)
		}
	}
}

func TestOIDCStateIsBoundToInitiatingBrowser(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, fragment := range []string{"__Host-admin_oidc", "SameSite: http.SameSiteLaxMode", "tokenMatches(tokenHash(correlation), state)", "Path: \"/\""} {
		if !strings.Contains(source, fragment) {
			t.Errorf("OIDC browser correlation missing %q", fragment)
		}
	}
}

func TestInvitedOIDCEnrollmentMigrationIsScopedAndFailClosed(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000012_invited_oidc_enrollment.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, fragment := range []string{
		"status IN ('invited','active','disabled','locked')",
		"purpose IN ('login','step_up','invitation')",
		"ALTER TABLE admin_invitation_enrollments FORCE ROW LEVEL SECURITY",
		"tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid",
		"merchant_id=nullif(current_setting('app.merchant_id',true),'')::uuid",
		"ensure_admin_invitation_identity",
		"lookup_admin_invitation_session(bytea,timestamptz)",
		"lookup_merchant_invitation_for_session(bytea,uuid,uuid,text,text,text)",
		"activate_admin_invitation_identity(uuid,uuid,uuid,text,text,text,timestamptz)",
		"cleanup_expired_admin_invitation_enrollments",
		"FOR UPDATE SKIP LOCKED",
		"lower(coalesce(found.email,''))<>normalized_email",
		"lower(coalesce(identity.email,''))<>lower(btrim(requested_email))",
		"purpose='admin'",
		"purpose='invitation'",
		"REVOKE ALL ON FUNCTION lookup_merchant_invitation(bytea) FROM PUBLIC",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("invited enrollment migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"token text", "invite_token", "GRANT INSERT ON admin_users", "DELETE FROM public.admin_users"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("invited enrollment migration contains unsafe capability %q", forbidden)
		}
	}
	down, err := os.ReadFile("../../migrations/000012_invited_oidc_enrollment.down.sql")
	downSQL := string(down)
	if err != nil || !strings.Contains(downSQL, "RETURNS TABLE(tenant_id uuid,merchant_id uuid)") || !strings.Contains(downSQL, "GRANT EXECUTE ON FUNCTION lookup_merchant_invitation(bytea) TO merchant_admin_runtime") {
		t.Fatal("down migration does not restore the previous lookup contract and runtime grant")
	}
	revokeAt := strings.Index(downSQL, "WHERE purpose='invitation'")
	disableAt := strings.Index(downSQL, "SET status='disabled'")
	dropAt := strings.Index(downSQL, "DROP TABLE IF EXISTS admin_invitation_enrollments")
	if revokeAt < 0 || disableAt < 0 || dropAt < 0 || revokeAt > dropAt || disableAt > dropAt {
		t.Fatal("rollback does not revoke invitation sessions and preserve invited identities as disabled before dropping enrollment state")
	}
}

func TestInvitationAcceptanceReplayIsSessionBound(t *testing.T) {
	migration, err := os.ReadFile("../../migrations/000012_invited_oidc_enrollment.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	for _, fragment := range []string{"i.status='accepted'", "e.user_id=requested_user", "e.session_id=requested_session", "e.status='accepted'", "e.oidc_issuer=requested_issuer", "e.oidc_subject=requested_subject", "s.purpose='admin'", "ON CONFLICT(invitation_id,user_id) DO UPDATE"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("acceptance replay binding missing %q", fragment)
		}
	}
	repository, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(repository)
	if !strings.Contains(source, "lookup_merchant_invitation_for_session($1,$2,$3,$4,$5,$6)") || !strings.Contains(source, "authenticated.Session.ID") {
		t.Fatal("BFF token lookup is not bound to the exact acceptance session")
	}
}

func TestInvitationRepositorySetsRLSBeforeEnrollmentMutation(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (r *PostgresRepository) CreateInvitationSession")
	if start < 0 {
		t.Fatal("invitation session repository method not found")
	}
	tail := source[start:]
	end := strings.Index(tail, "type execQuerier")
	if end < 0 {
		t.Fatal("invitation session repository method end not found")
	}
	method := tail[:end]
	setAt := strings.Index(method, "set_config('app.tenant_id'")
	insertAt := strings.Index(method, "INSERT INTO admin_invitation_enrollments")
	if setAt < 0 || insertAt < 0 || setAt > insertAt || !strings.Contains(method, "set_config('app.merchant_id'") {
		t.Fatal("enrollment mutation is not preceded by transaction-local tenant and merchant RLS context")
	}
}

func TestInvitationSessionLookupUsesNarrowDefinerBoundary(t *testing.T) {
	raw, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (r *PostgresRepository) FindInvitationSession")
	if start < 0 || !strings.Contains(source[start:], "FROM lookup_admin_invitation_session($1,$2)") {
		t.Fatal("invitation authentication bypasses the narrow RLS-safe session lookup")
	}
}
