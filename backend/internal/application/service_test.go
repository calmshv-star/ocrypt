package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/adapters/memory"
	"github.com/calmshv-star/ocrypt/backend/internal/application"
	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func principal() application.Principal {
	return application.Principal{TenantID: "tenant-a", MerchantID: "merchant-a", Scopes: map[string]bool{"payments:read": true, "payments:write": true}}
}

func TestCreateIntentIsIdempotentAndDetectsConflicts(t *testing.T) {
	store := memory.New()
	now := time.Unix(1_800_000_000, 0).UTC()
	svc := application.NewWithClock(store, fixedClock{now})
	cmd := application.CreateIntent{Principal: principal(), IdempotencyKey: "checkout-0001", MerchantOrderID: "order-1", AmountMinor: money.MustParse("3813"), Currency: "USD", CurrencyScale: 2, ExpiresAt: now.Add(time.Hour)}
	first, replay, err := svc.CreateIntent(context.Background(), cmd)
	if err != nil || replay {
		t.Fatalf("first create: replay=%v err=%v", replay, err)
	}
	second, replay, err := svc.CreateIntent(context.Background(), cmd)
	if err != nil || !replay || second.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", second, replay, err)
	}
	cmd.AmountMinor = money.MustParse("3814")
	if _, _, err := svc.CreateIntent(context.Background(), cmd); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	if got := len(store.Outbox()); got != 1 {
		t.Fatalf("replay created %d outbox events", got)
	}
}

func TestConcurrentExactAmountReservationHasSingleWinner(t *testing.T) {
	store := memory.New()
	now := time.Now().UTC()
	svc := application.NewWithClock(store, fixedClock{now})
	create := func(order, key string) string {
		p, _, err := svc.CreateIntent(context.Background(), application.CreateIntent{Principal: principal(), IdempotencyKey: key, MerchantOrderID: order, AmountMinor: money.MustParse("1000"), Currency: "USD", CurrencyScale: 2, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	ids := []string{create("order-a", "intent-key-a"), create("order-b", "intent-key-b")}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for i, id := range ids {
		go func(i int, id string) {
			defer wg.Done()
			_, _, err := svc.CreateRoute(context.Background(), application.CreateRoute{Principal: principal(), IntentID: id, IdempotencyKey: "route-key-" + string(rune('a'+i)) + "000", ChainID: "tron:mainnet", AssetID: "usdt-tron", ExpectedAmount: money.MustParse("10000001"), AssetDecimals: 6, DisplayAmount: "10.000001", Address: "TReceiver", RequiredFinality: 20, ExpiresAt: now.Add(time.Hour)})
			results <- err
		}(i, id)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("expected one reservation winner, got %d", success)
	}
}

func TestCancellationRetainsExactAmountReservationThroughGrace(t *testing.T) {
	store := memory.New()
	now := time.Now().UTC()
	svc := application.NewWithClock(store, fixedClock{now})
	create := func(order, key string) string {
		intent, _, err := svc.CreateIntent(context.Background(), application.CreateIntent{Principal: principal(), IdempotencyKey: key, MerchantOrderID: order, AmountMinor: money.MustParse("1000"), Currency: "USD", CurrencyScale: 2, ExpiresAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return intent.ID
	}
	first := create("cancel-release-a", "cancel-intent-a")
	route := func(intentID, key string) error {
		_, _, err := svc.CreateRoute(context.Background(), application.CreateRoute{Principal: principal(), IntentID: intentID, IdempotencyKey: key, ChainID: "tron:mainnet", AssetID: "usdt-tron", ExpectedAmount: money.MustParse("10000001"), AssetDecimals: 6, DisplayAmount: "10.000001", Address: "TReceiver", RequiredFinality: 20, ExpiresAt: now.Add(time.Hour)})
		return err
	}
	if err := route(first, "cancel-route-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CancelIntent(context.Background(), application.CancelIntent{Principal: principal(), IntentID: first, IdempotencyKey: "cancel-action-a", Reason: "customer request"}); err != nil {
		t.Fatal(err)
	}
	second := create("cancel-release-b", "cancel-intent-b")
	if err := route(second, "cancel-route-b-before-grace"); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("identical tuple was reusable before late-payment grace: %v", err)
	}
	if released, err := store.ReleaseElapsedRouteGrace(context.Background(), now.Add(26*time.Hour), 10); err != nil || released != 1 {
		t.Fatalf("grace sweeper released=%d err=%v", released, err)
	}
	if err := route(second, "cancel-route-b-after-grace"); err != nil {
		t.Fatalf("identical tuple was not reusable after grace: %v", err)
	}
}
