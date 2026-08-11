package main

import (
	"errors"
	"os"
	"strings"
)

type config struct {
	databaseURL                     string
	tlsCert                         string
	tlsKey                          string
	assertionSecretFile             string
	assertionIssuer                 string
	assertionAudience               string
	listenAddress                   string
	schedulerActorID                string
	schedulerWorkerID               string
	migrationActuatorDatabaseURL    string
	migrationManifestPublicKeysFile string
	migrationActuatorPublicKeysFile string
}

func loadConfig() (config, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", errors.New(name + " is required")
		}
		return value, nil
	}
	var c config
	var err error
	for _, item := range []struct {
		name   string
		target *string
	}{{"PLATFORM_ADMIN_DATABASE_URL", &c.databaseURL}, {"PLATFORM_ADMIN_TLS_CERT_FILE", &c.tlsCert}, {"PLATFORM_ADMIN_TLS_KEY_FILE", &c.tlsKey}, {"PLATFORM_ADMIN_ASSERTION_SECRET_FILE", &c.assertionSecretFile}, {"PLATFORM_ADMIN_ASSERTION_ISSUER", &c.assertionIssuer}, {"PLATFORM_ADMIN_ASSERTION_AUDIENCE", &c.assertionAudience}, {"PLATFORM_ADMIN_SCHEDULER_ACTOR_ID", &c.schedulerActorID}, {"PLATFORM_ADMIN_SCHEDULER_WORKER_ID", &c.schedulerWorkerID}, {"MIGRATION_ACTUATOR_DATABASE_URL", &c.migrationActuatorDatabaseURL}, {"MIGRATION_MANIFEST_PUBLIC_KEYS_FILE", &c.migrationManifestPublicKeysFile}, {"MIGRATION_ACTUATOR_PUBLIC_KEYS_FILE", &c.migrationActuatorPublicKeysFile}} {
		*item.target, err = required(item.name)
		if err != nil {
			return config{}, err
		}
	}
	c.listenAddress = strings.TrimSpace(os.Getenv("PLATFORM_ADMIN_LISTEN_ADDR"))
	if c.listenAddress == "" {
		c.listenAddress = "127.0.0.1:8446"
	}
	return c, nil
}
