package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/calmshv-star/ocrypt/backend/internal/providers"
)

var goLivePresets = map[string]bool{"strict": true, "standard": true, "fast": true}

// authorizeGoLive admits only a merchant-scoped owner grant.
func (s *Service) authorizeGoLive(principal Principal, scope Scope) error {
	if scope.MerchantID == "" {
		return ErrInvalid
	}
	_, err := principal.Authorize(PermissionGoLiveRequest, scope)
	return err
}

// CreateGoLiveChallenge returns the message the owner signs with the wallet
// that controls the receiving address.
func (s *Service) CreateGoLiveChallenge(principal Principal, scope Scope, domain, chainID, address string) (GoLiveProofChallenge, error) {
	if err := s.authorizeGoLive(principal, scope); err != nil {
		return GoLiveProofChallenge{}, err
	}
	chainID, display := strings.TrimSpace(chainID), strings.TrimSpace(address)
	kind, ok := goLiveProofKind(chainID)
	if !ok {
		return GoLiveProofChallenge{}, ErrInvalid
	}
	canonical, err := providers.CanonicalWatchAddress(chainID, display)
	if err != nil {
		return GoLiveProofChallenge{}, ErrInvalid
	}
	nonce, err := ids.New()
	if err != nil {
		return GoLiveProofChallenge{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	challenge := GoLiveProofChallenge{Kind: kind, ChainID: chainID, Address: canonical, Display: display, TenantID: scope.TenantID, MerchantID: scope.MerchantID, Nonce: nonce, IssuedAt: now, ExpiresAt: now.Add(walletProofTTL)}
	if challenge.Message, err = goLiveProofMessage(domain, challenge); err != nil {
		return GoLiveProofChallenge{}, err
	}
	sealed := challenge
	sealed.Message = ""
	payload, err := json.Marshal(sealed)
	if err != nil {
		return GoLiveProofChallenge{}, err
	}
	box, err := s.stateBox.Seal(payload)
	if err != nil {
		return GoLiveProofChallenge{}, err
	}
	challenge.Token = base64.RawURLEncoding.EncodeToString(box)
	return challenge, nil
}

func (s *Service) openGoLiveChallenge(token string) (GoLiveProofChallenge, error) {
	var challenge GoLiveProofChallenge
	if token == "" || len(token) > 8192 {
		return challenge, ErrInvalid
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return challenge, ErrInvalid
	}
	payload, err := s.stateBox.Open(sealed)
	if err != nil || json.Unmarshal(payload, &challenge) != nil {
		return GoLiveProofChallenge{}, ErrInvalid
	}
	return challenge, nil
}

// SubmitGoLive verifies every proof against the challenge it was issued for,
// for this merchant only, and files one go-live request with the preset. It
// needs an MFA session; the database refuses a second open request, a live
// merchant and a held signup.
func (s *Service) SubmitGoLive(ctx context.Context, principal Principal, scope Scope, domain string, input GoLiveSubmission) (GoLiveState, error) {
	if err := s.authorizeGoLive(principal, scope); err != nil {
		return GoLiveState{}, err
	}
	if err := principal.RequireStepUp(s.now(), s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
		return GoLiveState{}, err
	}
	if !goLivePresets[input.Preset] || len(input.Proofs) == 0 || len(input.Proofs) > 16 {
		return GoLiveState{}, ErrInvalid
	}
	now := s.now()
	seen := map[string]bool{}
	addresses := make([]GoLiveProvenAddress, 0, len(input.Proofs))
	for _, proof := range input.Proofs {
		challenge, err := s.openGoLiveChallenge(proof.Token)
		if err != nil || challenge.TenantID != scope.TenantID || challenge.MerchantID != scope.MerchantID || seen[challenge.ChainID] {
			return GoLiveState{}, ErrInvalid
		}
		seen[challenge.ChainID] = true
		message, err := verifyGoLiveProof(domain, challenge, proof.Signature, now)
		if err != nil {
			return GoLiveState{}, err
		}
		addresses = append(addresses, GoLiveProvenAddress{ChainID: challenge.ChainID, Canonical: challenge.Address, Display: challenge.Display, ProofKind: challenge.Kind, Nonce: challenge.Nonce, Message: message, Signature: strings.TrimSpace(proof.Signature), ProvenAt: now})
	}
	requestID, err := ids.New()
	if err != nil {
		return GoLiveState{}, err
	}
	changeID := ""
	if input.Preset != "strict" {
		if changeID, err = ids.New(); err != nil {
			return GoLiveState{}, err
		}
	}
	if err := s.repository.SubmitGoLive(ctx, principal, scope, requestID, input.Preset, addresses, changeID, now); err != nil {
		return GoLiveState{}, err
	}
	return s.repository.GoLiveState(ctx, principal, scope)
}

func (s *Service) GoLiveState(ctx context.Context, principal Principal, scope Scope) (GoLiveState, error) {
	if err := s.authorizeGoLive(principal, scope); err != nil {
		return GoLiveState{}, err
	}
	return s.repository.GoLiveState(ctx, principal, scope)
}

// ListGoLiveReviews returns pending go-live requests to a global reviewer.
func (s *Service) ListGoLiveReviews(ctx context.Context, principal Principal) ([]GoLiveReview, error) {
	if _, err := principal.AuthorizePlatform(PermissionGoLiveReviewsRead, ""); err != nil {
		return nil, err
	}
	return s.repository.ListGoLiveReviews(ctx, principal, 100)
}

// DecideGoLiveReview approves or rejects one request with a reason of at least
// eight characters (the matching-policy approval minimum).
func (s *Service) DecideGoLiveReview(ctx context.Context, principal Principal, requestID string, input SignupDecision) (string, error) {
	if _, err := principal.AuthorizePlatform(PermissionGoLiveReviewsDecide, ""); err != nil {
		return "", err
	}
	if err := principal.RequireStepUp(s.now(), s.config.RequiredACR, s.config.AcceptedAMR); err != nil {
		return "", err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !ids.Valid(requestID) || (input.Decision != "approve" && input.Decision != "reject") || utf8.RuneCountInString(input.Reason) < 8 || utf8.RuneCountInString(input.Reason) > 1000 || strings.IndexFunc(input.Reason, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf) }) >= 0 {
		return "", ErrInvalid
	}
	return s.repository.DecideGoLiveReview(ctx, principal, requestID, input, s.now())
}
