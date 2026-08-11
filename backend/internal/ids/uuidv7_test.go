package ids_test

import (
	"testing"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
)

func TestUUIDValidationRejectsDatabaseCastInputs(t *testing.T) {
	generated, err := ids.New()
	if err != nil || !ids.Valid(generated) {
		t.Fatalf("generated UUID invalid: %q %v", generated, err)
	}
	for _, invalid := range []string{"", "not-a-uuid", "019FED4B-47E6-74C4-B79E-76363FB73BCD", "019fed4b47e674c4b79e76363fb73bcd", "019fed4b-47e6-04c4-b79e-76363fb73bcd", "019fed4b-47e6-74c4-779e-76363fb73bcd"} {
		if ids.Valid(invalid) {
			t.Fatalf("invalid UUID accepted: %q", invalid)
		}
	}
}
