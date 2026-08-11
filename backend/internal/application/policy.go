package application

import (
	"fmt"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

type ExceptionAction string

const (
	ActionSettle             ExceptionAction = "settle"
	ActionAccumulate         ExceptionAction = "accumulate"
	ActionManualReview       ExceptionAction = "manual_review"
	ActionSettleRefundExcess ExceptionAction = "settle_refund_excess"
	ActionSettleCreditExcess ExceptionAction = "settle_credit_excess"
)

type OverpaymentPolicy string

const (
	OverpaymentReview       OverpaymentPolicy = "review"
	OverpaymentRefundExcess OverpaymentPolicy = "refund_excess"
	OverpaymentCreditExcess OverpaymentPolicy = "credit_excess"
)

type ExceptionPolicy struct {
	Version                   int64
	AccumulatePartial         bool
	AutoAcceptUnderpaymentBPS uint32
	AcceptExactWithinGrace    bool
	Overpayment               OverpaymentPolicy
}

type ExceptionAssessment struct {
	Class         ExceptionClass  `json:"class"`
	Action        ExceptionAction `json:"action"`
	PolicyVersion int64           `json:"policy_version"`
	AmountDelta   string          `json:"amount_delta"`
	ReasonCodes   []string        `json:"reason_codes"`
}

// AssessException is a deterministic, versioned policy decision. It never
// performs fuzzy identity matching and never permits AI output to authorize a
// financial action.
func AssessException(event domain.TransferEvent, route domain.PaymentRoute, now time.Time, policy ExceptionPolicy) (ExceptionAssessment, error) {
	if policy.Version < 1 || policy.AutoAcceptUnderpaymentBPS > 10_000 {
		return ExceptionAssessment{}, fmt.Errorf("%w: invalid exception policy", domain.ErrValidation)
	}
	assessment := ExceptionAssessment{PolicyVersion: policy.Version, Action: ActionManualReview}
	if route.ChainID != event.Identity.ChainID || route.Address != event.Identity.ToAddress {
		assessment.Class = ExceptionUnmatched
		assessment.ReasonCodes = []string{"route_identity_mismatch"}
		return assessment, nil
	}
	if route.AssetID != event.Identity.AssetID {
		assessment.Class = ExceptionWrongAsset
		assessment.ReasonCodes = []string{"wrong_asset", "conversion_quote_required", "four_eyes_required"}
		return assessment, nil
	}
	cmp := event.Amount.Cmp(route.ExpectedAmount)
	late := event.OnChainTime.After(route.ExpiresAt)
	withinGrace := !event.OnChainTime.After(route.GraceEndsAt)
	if cmp == 0 {
		if late {
			assessment.Class = ExceptionLate
			assessment.ReasonCodes = []string{"after_expiry"}
			if withinGrace && policy.AcceptExactWithinGrace {
				assessment.Action = ActionSettle
				assessment.ReasonCodes = append(assessment.ReasonCodes, "within_grace_policy")
			}
			return assessment, nil
		}
		assessment.Class = ExceptionExact
		assessment.Action = ActionSettle
		assessment.ReasonCodes = []string{"exact_amount", "within_payment_window"}
		return assessment, nil
	}
	if cmp < 0 {
		shortfall, _ := route.ExpectedAmount.Sub(event.Amount)
		assessment.AmountDelta = "-" + shortfall.String()
		if now.Before(route.ExpiresAt) && policy.AccumulatePartial {
			assessment.Class = ExceptionPartial
			assessment.Action = ActionAccumulate
			assessment.ReasonCodes = []string{"below_expected", "route_still_open"}
			return assessment, nil
		}
		assessment.Class = ExceptionUnderpaid
		assessment.ReasonCodes = []string{"below_expected", "shortfall_review"}
		if underpaymentWithinTolerance(event.Amount, route.ExpectedAmount, policy.AutoAcceptUnderpaymentBPS) {
			assessment.Action = ActionSettle
			assessment.ReasonCodes = append(assessment.ReasonCodes, "within_versioned_tolerance")
		}
		return assessment, nil
	}
	excess, _ := event.Amount.Sub(route.ExpectedAmount)
	assessment.Class = ExceptionOverpaid
	assessment.AmountDelta = excess.String()
	assessment.ReasonCodes = []string{"above_expected"}
	switch policy.Overpayment {
	case OverpaymentRefundExcess:
		assessment.Action = ActionSettleRefundExcess
	case OverpaymentCreditExcess:
		assessment.Action = ActionSettleCreditExcess
	default:
		assessment.Action = ActionManualReview
	}
	return assessment, nil
}

func underpaymentWithinTolerance(received, expected money.Amount, toleranceBPS uint32) bool {
	if toleranceBPS == 0 {
		return false
	}
	shortfall, err := expected.Sub(received)
	if err != nil {
		return false
	}
	maximum, err := expected.MulDivFloor(money.MustParse(fmt.Sprintf("%d", toleranceBPS)), money.MustParse("10000"))
	return err == nil && shortfall.Cmp(maximum) <= 0
}
