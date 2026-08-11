package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

func TestPaymentLifecycleMigrationIsDurableAndFailClosed(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/000023_payment_lifecycle_webhooks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, marker := range []string{
		"CREATE TABLE payment_observations",
		"transfer_event_id uuid NOT NULL UNIQUE",
		"required_confirmations bigint NOT NULL CHECK(required_confirmations>0)",
		"CREATE TABLE payment_observation_events",
		"PRIMARY KEY(observation_id,generation,event_type)",
		"CREATE TABLE manual_resolution_events",
		"manual_resolutions_id_tenant_unique UNIQUE(id,tenant_id)",
		"FOREIGN KEY(resolution_id,tenant_id) REFERENCES manual_resolutions(id,tenant_id)",
		"AFTER INSERT OR UPDATE OF status ON manual_resolutions",
		"'manual_resolution',NEW.id,NEW.version,NEW.version",
		"aggregate_sequence,occurred_at,created_at)",
		"w.id,w.signing_key_id,'pending'",
		"OLD.finality='finalized' AND NEW.finality='reorged'",
		"OLD.finality='reorged'",
		"NEW.generation=OLD.generation+1",
		"NEW.finality IN ('observed','confirmed','finalized')",
		"ALTER TABLE payment_observations FORCE ROW LEVEL SECURITY",
		"REVOKE DELETE,TRUNCATE",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("000023 lost lifecycle invariant %q", marker)
		}
	}
	if strings.Contains(sql, "occurred timestamptz:=NEW.updated_at") {
		t.Fatal("resolution webhook trusts an application-supplied timestamp")
	}
	if !strings.Contains(sql, "occurred timestamptz:=clock_timestamp()") {
		t.Fatal("resolution webhook must use the database clock")
	}
	if strings.Contains(sql, "merchant_resolution_worker','merchant_scanner_worker") || strings.Contains(sql, "GRANT SELECT,INSERT,UPDATE ON payment_observations TO merchant_resolution_worker") {
		t.Fatal("manual resolution worker can mutate on-chain observations")
	}
	if !strings.Contains(sql, "ARRAY['merchant_scanner_worker','merchant_settlement_worker']") || !strings.Contains(sql, "GRANT SELECT ON manual_resolution_events TO merchant_resolution_worker") {
		t.Fatal("000023 lost least-privilege lifecycle grants")
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "BEGIN;") || !strings.HasSuffix(strings.TrimSpace(sql), "COMMIT;") {
		t.Fatal("000023 must be fail-atomic")
	}
}

func TestTransferProgressDeduplicatesAndRejectsStaleFinality(t *testing.T) {
	status, confirmations, actionable, err := mergeTransferProgress(domain.TransferObserved, 1, domain.TransferObserved, 1)
	if err != nil || actionable || status != domain.TransferObserved || confirmations != 1 {
		t.Fatalf("duplicate observation advanced: %s %d %v %v", status, confirmations, actionable, err)
	}
	status, confirmations, actionable, err = mergeTransferProgress(domain.TransferObserved, 2, domain.TransferObserved, 1)
	if err != nil || actionable || confirmations != 2 {
		t.Fatalf("out-of-order confirmation regressed: %d %v %v", confirmations, actionable, err)
	}
	status, confirmations, actionable, err = mergeTransferProgress(domain.TransferObserved, 2, domain.TransferConfirmed, 3)
	if err != nil || !actionable || status != domain.TransferConfirmed || confirmations != 3 {
		t.Fatalf("confirmation milestone was lost: %s %d %v %v", status, confirmations, actionable, err)
	}
	if _, _, _, err = mergeTransferProgress(domain.TransferConfirmed, 3, domain.TransferObserved, 4); err == nil {
		t.Fatal("stale finality label regressed a confirmed transfer")
	}
}

func TestObservationCallbackDecisionDoesNotSynthesizeHistory(t *testing.T) {
	tests := []struct {
		name                       string
		created, reincluded        bool
		finality                   string
		confirmingAlreadyDelivered bool
		transfer                   domain.TransferStatus
		want                       string
	}{
		{"first observed", true, false, "observed", false, domain.TransferObserved, "payment.observed"},
		{"confirmed first", true, false, "confirmed", false, domain.TransferConfirmed, "payment.confirming"},
		{"policy milestone", false, false, "confirmed", false, domain.TransferObserved, "payment.confirming"},
		{"duplicate milestone", false, false, "confirmed", true, domain.TransferConfirmed, ""},
		{"reincluded", false, true, "observed", false, domain.TransferObserved, "payment.observed"},
		{"finalized first", true, false, "finalized", false, domain.TransferFinalized, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := observationLifecycleEvent(test.created, test.reincluded, test.finality, test.confirmingAlreadyDelivered, test.transfer); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}

func TestProductionObservationPathIsTransitionOnlyAndReorgAware(t *testing.T) {
	observationRaw, err := os.ReadFile("payment_observation.go")
	if err != nil {
		t.Fatal(err)
	}
	settlementRaw, err := os.ReadFile("settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	reorgRaw, err := os.ReadFile("reorg.go")
	if err != nil {
		t.Fatal(err)
	}
	observation := string(observationRaw)
	for _, marker := range []string{
		"payment.observed", "payment.confirming", "payment.reorged",
		"event.Confirmations >= candidate.RequiredFinality",
		"payment_observation_events WHERE observation_id=$1 AND generation=$2",
		"aggregate_type,aggregate_id,aggregate_version,aggregate_sequence",
		"'payment_observation'",
		"endpoint_id,signing_key_id,status",
		"observation.Finality == \"reorged\"",
		"status='reorg_review'",
		"payment lifecycle migration 000023 is not ready",
	} {
		if !strings.Contains(observation, marker) {
			t.Fatalf("observation runtime lost %q", marker)
		}
	}
	if strings.Contains(observation, "status='pending'") {
		t.Fatal("reorg regresses an observed intent to pending")
	}
	if !strings.Contains(string(settlementRaw), "resultStatus != current || resultConfirmations != confirmations") {
		t.Fatal("confirmation progression never reaches the policy-milestone evaluator")
	}
	if !strings.Contains(string(reorgRaw), "reorgPaymentObservation(ctx, tx, eventID, len(settlements) == 0") {
		t.Fatal("reorg path ignores observations without a settled payment_match")
	}
	if !strings.Contains(observation, `if status == "observed" || status == "confirmed"`) || !strings.Contains(observation, `emitObservationCallback(ctx, tx, candidate, observation, "payment.reorged", status, version, now)`) {
		t.Fatal("pre-settlement reorg does not preserve legitimate non-observed intent states")
	}
	if !strings.Contains(string(reorgRaw), `linkObservationEvent(ctx, tx, observation, "payment.reorged", webhookID, outboxID, now)`) {
		t.Fatal("settled reorg callback/outbox is not durably linked to its observation")
	}
	for name, source := range map[string]string{"settlement": string(settlementRaw), "reorg": string(reorgRaw)} {
		if !strings.Contains(source, "WHERE tenant_id=$1 AND merchant_id=$2 AND status='active'") {
			t.Fatalf("%s lifecycle webhook endpoint query is not tenant scoped", name)
		}
	}
}
