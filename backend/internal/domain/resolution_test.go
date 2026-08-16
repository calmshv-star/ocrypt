package domain

import "testing"

func TestManualFinancialOverridesUseOneOperatorAndIndependentChainVerification(t *testing.T) {
	r := ManualResolution{RequestedBy: "operator-a", AcceptShortfall: true}
	if RequiresFourEyes(r) || !r.ApprovalIsValid() {
		t.Fatal("shortfall override unexpectedly required a second operator")
	}
}

func TestLateExactAssetResolutionCanFollowPolicyWithoutFourEyes(t *testing.T) {
	r := ManualResolution{RequestedBy: "operator-a", AcceptLatePayment: true}
	if RequiresFourEyes(r) || !r.ApprovalIsValid() {
		t.Fatal("late-only resolution unexpectedly required a second operator")
	}
}
