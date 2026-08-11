package main

import (
	"context"
	"errors"
	"testing"
)

type readinessFixture struct {
	err   error
	calls int
}

func (fixture *readinessFixture) Ping(context.Context) error {
	fixture.calls++
	return fixture.err
}

func (fixture *readinessFixture) HostedProviderReady(context.Context) error {
	fixture.calls++
	return fixture.err
}

func TestReadinessGroupFailsClosedOnHostedMigrationOrGrantFailure(t *testing.T) {
	database := &readinessFixture{}
	hosted := &readinessFixture{err: errors.New("required EXECUTE grant was revoked")}
	group := readinessGroup{database, hostedProviderProbe{store: hosted}}
	if err := group.Ping(context.Background()); err == nil {
		t.Fatal("readiness accepted a hosted provider capability failure")
	}
	if database.calls != 1 || hosted.calls != 1 {
		t.Fatalf("readiness calls = database:%d hosted:%d", database.calls, hosted.calls)
	}
}

func TestReadinessGroupAcceptsAllDependencies(t *testing.T) {
	database := &readinessFixture{}
	hosted := &readinessFixture{}
	if err := (readinessGroup{database, hostedProviderProbe{store: hosted}}).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
