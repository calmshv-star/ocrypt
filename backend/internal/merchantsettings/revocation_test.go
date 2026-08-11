package merchantsettings

import (
	"context"
	"errors"
	"testing"
	"time"
)

type revocationFake struct {
	count int
	err   error
	ping  error
}

func (f *revocationFake) ConsumeSessionRevocations(context.Context, int) (int, error) {
	return f.count, f.err
}
func (f *revocationFake) Ping(context.Context) error { return f.ping }
func TestRevocationWorkerReadinessRequiresSuccessfulRecentPoll(t *testing.T) {
	store := &revocationFake{count: 2}
	worker, err := NewRevocationWorker(store, 100)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Ready(context.Background(), time.Second) == nil {
		t.Fatal("worker ready before first poll")
	}
	if n, err := worker.Tick(context.Background()); err != nil || n != 2 {
		t.Fatal("tick failed")
	}
	if err = worker.Ready(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("database unavailable")
	_, _ = worker.Tick(context.Background())
	if worker.Ready(context.Background(), time.Second) == nil {
		t.Fatal("worker stayed ready after failed poll")
	}
}
