// migration-control is the offline, non-mutating manifest admission tool.
// Live requests intentionally go through the platform-admin API so this CLI
// cannot bypass OIDC step-up, two-person approval or request-bound assertions.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/migrationcontrol"
)

func main() {
	manifestFile := flag.String("manifest", "", "signed migration manifest JSON")
	keysFile := flag.String("public-keys", "", "authorized Ed25519 public key ring")
	execute := flag.Bool("execute", false, "not supported; use the authenticated platform-admin API")
	flag.Parse()
	if err := run(*manifestFile, *keysFile, *execute); err != nil {
		fmt.Fprintln(os.Stderr, "migration manifest rejected:", err)
		os.Exit(1)
	}
}

func run(manifestFile, keysFile string, execute bool) error {
	if execute {
		return errors.New("offline CLI cannot execute; submit through platform-admin with fresh MFA")
	}
	if manifestFile == "" || keysFile == "" {
		return errors.New("--manifest and --public-keys are required")
	}
	b, err := os.ReadFile(manifestFile)
	if err != nil || len(b) > migrationcontrol.MaxManifestBytes+(32<<10) {
		return errors.New("read bounded signed manifest")
	}
	document, err := migrationcontrol.DecodeSignedManifest(b)
	if err != nil {
		return errors.New("decode signed manifest")
	}
	keys, err := migrationcontrol.ReadPublicKeyRing(keysFile)
	if err != nil {
		return err
	}
	manifest, _, digest, signers, err := migrationcontrol.ParseAndVerify(document, keys, time.Now().UTC())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"dry_run": true, "admissible": true, "manifest_id": manifest.ManifestID, "migration_id": manifest.MigrationID, "kind": manifest.Kind, "payload_hash": digest, "signer_key_ids": signers})
}
