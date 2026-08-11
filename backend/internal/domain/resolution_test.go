package domain

import "testing"

func TestFinancialOverridesRequireIndependentApprover(t *testing.T) {
	r := ManualResolution{RequestedBy: "operator-a", AcceptShortfall: true}
	if r.ApprovalIsValid() {
		t.Fatal("shortfall override passed without approval")
	}
	r.ApprovedBy = "operator-a"
	if r.ApprovalIsValid() {
		t.Fatal("self-approval passed four-eyes control")
	}
	r.ApprovedBy = "operator-b"
	if !r.ApprovalIsValid() {
		t.Fatal("independent approval was rejected")
	}
}

func TestLateExactAssetResolutionCanFollowPolicyWithoutFourEyes(t *testing.T) {
	r := ManualResolution{RequestedBy: "operator-a", AcceptLatePayment: true}
	if RequiresFourEyes(r) || !r.ApprovalIsValid() {
		t.Fatal("late-only resolution unexpectedly required a second operator")
	}
}
