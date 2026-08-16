package domain

import "time"

type UnmatchedStatus string

const (
	UnmatchedNew                   UnmatchedStatus = "new"
	UnmatchedCandidatesReady       UnmatchedStatus = "candidates_ready"
	UnmatchedBound                 UnmatchedStatus = "bound"
	UnmatchedApprovalRequired      UnmatchedStatus = "approval_required"
	UnmatchedVerificationRequested UnmatchedStatus = "verification_requested"
	UnmatchedVerificationRetry     UnmatchedStatus = "verification_retry"
	UnmatchedVerified              UnmatchedStatus = "verified"
	UnmatchedResolved              UnmatchedStatus = "resolved"
	UnmatchedIgnored               UnmatchedStatus = "ignored"
	UnmatchedInvalid               UnmatchedStatus = "invalid"
	UnmatchedConflict              UnmatchedStatus = "conflict"
	UnmatchedReorged               UnmatchedStatus = "reorged"
)

type UnmatchedPayment struct {
	ID                  string          `json:"id"`
	TransferEventID     string          `json:"transfer_event_id"`
	Classification      string          `json:"classification"`
	Status              UnmatchedStatus `json:"status"`
	SelectedRouteID     string          `json:"selected_route_id,omitempty"`
	AcceptedShortfall   bool            `json:"accepted_shortfall"`
	AcceptedLatePayment bool            `json:"accepted_late_payment"`
	AcceptedCrossAsset  bool            `json:"accepted_cross_asset"`
	WorkflowVersion     int64           `json:"workflow_version"`
	AssignedOperatorID  string          `json:"assigned_operator_id,omitempty"`
	Version             int64           `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type MatchCandidate struct {
	ID                  string         `json:"id"`
	UnmatchedPaymentID  string         `json:"unmatched_payment_id"`
	RouteID             string         `json:"route_id"`
	Rank                uint32         `json:"rank"`
	Score               int32          `json:"score"`
	Evidence            map[string]any `json:"evidence"`
	Disqualifiers       []string       `json:"disqualifiers"`
	CandidateSetVersion int64          `json:"candidate_set_version"`
}

type ManualResolution struct {
	ID                   string          `json:"id"`
	UnmatchedPaymentID   string          `json:"unmatched_payment_id"`
	TransferEventID      string          `json:"transfer_event_id"`
	TargetRouteID        string          `json:"target_route_id"`
	CandidateSetVersion  int64           `json:"candidate_set_version"`
	IdempotencyKey       string          `json:"idempotency_key"`
	RequestedBy          string          `json:"requested_by"`
	ApprovedBy           string          `json:"approved_by,omitempty"`
	AcceptShortfall      bool            `json:"accept_shortfall"`
	AcceptLatePayment    bool            `json:"accept_late_payment"`
	AcceptCrossAsset     bool            `json:"accept_cross_asset"`
	Reason               string          `json:"reason"`
	Status               UnmatchedStatus `json:"status"`
	VerifierEvidenceHash string          `json:"verifier_evidence_hash,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	Version              int64           `json:"version"`
	ClaimToken           string          `json:"-"`
	Attempt              int             `json:"-"`
}

func RequiresFourEyes(r ManualResolution) bool {
	return false
}

func (r ManualResolution) ApprovalIsValid() bool {
	return true
}
