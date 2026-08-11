package application

import (
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
	"github.com/calmshv-star/ocrypt/backend/internal/money"
)

func TestExceptionPolicyCoversPartialUnderOverLateAndWrongAsset(t *testing.T) {
	now := time.Now().UTC()
	route := domain.PaymentRoute{ChainID: "tron", AssetID: "usdt", Address: "merchant", ExpectedAmount: money.MustParse("10000"), StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), GraceEndsAt: now.Add(25 * time.Hour)}
	event := domain.TransferEvent{Identity: domain.EventIdentity{ChainID: "tron", AssetID: "usdt", ToAddress: "merchant"}, Amount: money.MustParse("9900"), OnChainTime: now}
	policy := ExceptionPolicy{Version: 7, AccumulatePartial: true, AutoAcceptUnderpaymentBPS: 100, AcceptExactWithinGrace: true, Overpayment: OverpaymentRefundExcess}
	assessment, err := AssessException(event, route, now, policy)
	if err != nil || assessment.Class != ExceptionPartial || assessment.Action != ActionAccumulate {
		t.Fatalf("partial: %+v err=%v", assessment, err)
	}
	assessment, _ = AssessException(event, route, route.ExpiresAt.Add(time.Second), policy)
	if assessment.Class != ExceptionUnderpaid || assessment.Action != ActionSettle {
		t.Fatalf("underpayment tolerance: %+v", assessment)
	}
	event.Amount = money.MustParse("10100")
	assessment, _ = AssessException(event, route, now, policy)
	if assessment.Class != ExceptionOverpaid || assessment.Action != ActionSettleRefundExcess {
		t.Fatalf("overpayment: %+v", assessment)
	}
	event.Amount = route.ExpectedAmount
	event.OnChainTime = route.ExpiresAt.Add(time.Second)
	assessment, _ = AssessException(event, route, now, policy)
	if assessment.Class != ExceptionLate || assessment.Action != ActionSettle {
		t.Fatalf("late exact within grace: %+v", assessment)
	}
	event.Identity.AssetID = "trx"
	assessment, _ = AssessException(event, route, now, policy)
	if assessment.Class != ExceptionWrongAsset || assessment.Action != ActionManualReview {
		t.Fatalf("wrong asset: %+v", assessment)
	}
}
