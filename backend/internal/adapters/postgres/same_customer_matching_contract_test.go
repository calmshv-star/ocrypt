package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestSameCustomerCandidateContextContractIsBoundedLockedAndPersistent(t *testing.T) {
	settlement, err := os.ReadFile("settlement.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ORDER BY r.created_at DESC,r.id LIMIT 101",
		"truncated := len(routes) > 100",
		"routes = routes[:100]",
		`candidates[index].Reasons = append(candidates[index].Reasons, "candidate_context_truncated")`,
		`"reason_codes": candidate.Reasons`,
		"application.SelectAutomaticCandidate(event, potential, contexts)",
	} {
		if !strings.Contains(string(settlement), required) {
			t.Errorf("bounded persistent candidate context contract missing %q", required)
		}
	}
	loader, err := os.ReadFile("same_customer_matching.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"i.tenant_id=r.tenant_id AND i.merchant_id=r.merchant_id", "r.id=ANY($1::uuid[]) AND r.tenant_id=$2", "ORDER BY r.id FOR UPDATE OF r,i"} {
		if !strings.Contains(string(loader), required) {
			t.Errorf("authoritative locked context contract missing %q", required)
		}
	}
	matching, err := os.ReadFile("matching_automation.go")
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.SplitN(string(matching), "func loadAutomaticEventOwner", 2)[1]
	owner = strings.SplitN(owner, "func failClosedAmbiguousDecision", 2)[0]
	for _, required := range []string{"ORDER BY mc.score DESC,mc.rank", "loadAutomaticCandidateContexts(ctx, tx, candidates, tenantID)", "application.SelectAutomaticCandidate(event, candidates, contexts)"} {
		if !strings.Contains(owner, required) {
			t.Errorf("complete persisted ownership revalidation missing %q", required)
		}
	}
	if strings.Contains(owner, "LIMIT 2") {
		t.Fatal("ownership revalidation silently ignored additional tied contenders")
	}
}
