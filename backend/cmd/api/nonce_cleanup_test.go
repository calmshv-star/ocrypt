package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type nonceCleanerFixture struct {
	counts []int64
	calls  int
	err    error
}

func (fixture *nonceCleanerFixture) DeleteExpiredNonces(context.Context, int) (int64, error) {
	fixture.calls++
	if fixture.err != nil {
		return 0, fixture.err
	}
	if len(fixture.counts) == 0 {
		return 0, nil
	}
	count := fixture.counts[0]
	fixture.counts = fixture.counts[1:]
	return count, nil
}

func TestNonceCleanupPassIsBoundedAndDrainsShortFinalBatch(t *testing.T) {
	fixture := &nonceCleanerFixture{counts: []int64{100, 100, 7}}
	removed, err := runNonceCleanupPass(t.Context(), fixture, nonceCleanupConfig{BatchSize: 100, MaxBatches: 5})
	if err != nil || removed != 207 || fixture.calls != 3 {
		t.Fatalf("removed=%d calls=%d err=%v", removed, fixture.calls, err)
	}

	fixture = &nonceCleanerFixture{counts: []int64{100, 100, 100}}
	removed, err = runNonceCleanupPass(t.Context(), fixture, nonceCleanupConfig{BatchSize: 100, MaxBatches: 2})
	if err != nil || removed != 200 || fixture.calls != 2 {
		t.Fatalf("bounded removed=%d calls=%d err=%v", removed, fixture.calls, err)
	}
}

func TestNonceCleanupConfigurationFailsClosed(t *testing.T) {
	if _, err := loadNonceCleanupConfig("30s", "10000", "5"); err == nil {
		t.Fatal("expected too-frequent cleanup rejection")
	}
	if _, err := loadNonceCleanupConfig("1m", "100001", "5"); err == nil {
		t.Fatal("expected oversized batch rejection")
	}
	if _, err := loadNonceCleanupConfig("1m", "10000", "21"); err == nil {
		t.Fatal("expected excessive pass rejection")
	}
	config, err := loadNonceCleanupConfig("", "", "")
	if err != nil || config.Interval != time.Minute || config.BatchSize != 10_000 || config.MaxBatches != 5 {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestNonceCleanupPassReturnsDatabaseError(t *testing.T) {
	expected := errors.New("database unavailable")
	removed, err := runNonceCleanupPass(t.Context(), &nonceCleanerFixture{err: expected}, nonceCleanupConfig{BatchSize: 100, MaxBatches: 1})
	if removed != 0 || !errors.Is(err, expected) {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}
