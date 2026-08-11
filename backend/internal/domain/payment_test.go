package domain

import (
	"errors"
	"testing"
	"time"
)

func TestIntentStateMachineRejectsUnsafeRegression(t *testing.T) {
	now := time.Now()
	p := PaymentIntent{ID: "pi_test", Status: IntentSettled, Version: 4}
	if err := p.Transition(IntentPending, "", now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state conflict, got %v", err)
	}
	if p.Status != IntentSettled || p.Version != 4 {
		t.Fatal("failed transition mutated the aggregate")
	}
}

func TestIntentStateMachineUsesCompensatingReorgFlow(t *testing.T) {
	now := time.Now()
	p := PaymentIntent{ID: "pi_test", Status: IntentSettled, Version: 4}
	if err := p.Transition(IntentReorgReview, "deep_reorg", now); err != nil {
		t.Fatal(err)
	}
	if err := p.Transition(IntentReversed, "event_not_canonical", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if p.Status != IntentReversed || p.Version != 6 {
		t.Fatalf("unexpected state: %+v", p)
	}
}
