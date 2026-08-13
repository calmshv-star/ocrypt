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
		"status='active'",
		"'*'=ANY(event_types)",
		"event_types @> `+requiredWebhookEventTypesSQL+`",
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
