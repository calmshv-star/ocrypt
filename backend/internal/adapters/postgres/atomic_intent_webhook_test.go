package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestAtomicIntentRequiresLifecycleWebhook(t *testing.T) {
	source, err := os.ReadFile("atomic_intent.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, marker := range []string{
		"w.status='active'",
		"v.verified_at IS NOT NULL",
		"'*'=ANY(w.event_types)",
		"w.event_types @> `+requiredWebhookEventTypesSQL+`",
		"payment.partially_paid",
		"payment.needs_review",
		"payment.settled",
		"payment.overpaid",
		"payment.expired",
		"payment.cancelled",
		"payment.reorged",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("atomic intent admission is missing %q", marker)
		}
	}
	if strings.Index(text, "webhookReady") > strings.Index(text, "INSERT INTO payment_intents") {
		t.Fatal("webhook readiness must fail before a payment intent is inserted")
	}
}

func TestVerifiedWebhookAdmissionMigrationGrantsOnlyReadEvidence(t *testing.T) {
	up, err := os.ReadFile("../../../migrations/000042_verified_webhook_admission.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	if !strings.Contains(text, "GRANT SELECT ON management_webhook_verifications TO merchant_api_runtime") {
		t.Fatal("merchant API must receive read-only access to verification evidence")
	}
	for _, forbidden := range []string{"GRANT INSERT", "GRANT UPDATE", "GRANT DELETE", "BYPASSRLS"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("verification admission migration contains forbidden privilege %q", forbidden)
		}
	}
}
