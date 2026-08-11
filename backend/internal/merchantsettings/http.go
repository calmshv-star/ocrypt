package merchantsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type principalContextKey struct{}

type Server struct {
	service       *Service
	authenticator Authenticator
	mux           *http.ServeMux
	bodyLimit     int64
}

func NewServer(service *Service, authenticator Authenticator, bodyLimit int64) (*Server, error) {
	if service == nil || authenticator == nil || bodyLimit < 1024 || bodyLimit > 1<<20 {
		return nil, ErrInvalid
	}
	s := &Server{service: service, authenticator: authenticator, mux: http.NewServeMux(), bodyLimit: bodyLimit}
	s.mux.HandleFunc("GET /v1/merchant-cabinet/roles", s.auth(s.listRoles))
	s.mux.HandleFunc("GET /v1/merchant-cabinet/members", s.auth(s.listMembers))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/members/{id}/roles", s.auth(s.replaceRoles))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/members/{id}/disable", s.auth(s.disableMember))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/members/{id}/remove", s.auth(s.removeMember))
	s.mux.HandleFunc("GET /v1/merchant-cabinet/invitations", s.auth(s.listInvitations))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/invitations", s.auth(s.createInvitation))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/invitations/{id}/revoke", s.auth(s.revokeInvitation))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/invitations/accept", s.auth(s.acceptInvitation))
	s.mux.HandleFunc("GET /v1/merchant-cabinet/security-actions", s.auth(s.listActions))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/security-actions", s.auth(s.createAction))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/security-actions/{id}/approve", s.auth(s.approveAction))
	s.mux.HandleFunc("POST /v1/merchant-cabinet/security-actions/{id}/reject", s.auth(s.rejectAction))
	s.mux.HandleFunc("GET /v1/merchant-cabinet/settings", s.auth(s.getSettings))
	s.mux.HandleFunc("PUT /v1/merchant-cabinet/settings", s.auth(s.updateSettings))
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.mux }

type apiHandler func(http.ResponseWriter, *http.Request, []byte)

func (s *Server) auth(next apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		required := r.Method != "GET"
		body, ok := s.readBody(w, r, required)
		if !ok {
			return
		}
		p, err := s.authenticator.Authenticate(r.Context(), r, body)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p)), body)
	}
}
func principal(r *http.Request) Principal {
	p, _ := r.Context().Value(principalContextKey{}).(Principal)
	return p
}
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, required bool) ([]byte, bool) {
	if !required {
		return nil, true
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		writeProblem(w, 400, "invalid_request")
		return nil, false
	}
	media, params, err := mime.ParseMediaType(values[0])
	if err != nil || strings.ToLower(media) != "application/json" || len(params) != 0 {
		writeProblem(w, 400, "invalid_request")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.bodyLimit))
	if err != nil || len(body) == 0 || validateUniqueJSON(body) != nil {
		writeProblem(w, 400, "invalid_request")
		return nil, false
	}
	return body, true
}
func decode(body []byte, target any) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil && d.Decode(&struct{}{}) == io.EOF
}
func validateUniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := walkJSON(d); err != nil {
		return err
	}
	_, err := d.Token()
	if err != io.EOF {
		return ErrInvalid
	}
	return nil
}
func walkJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			key, valid := k.(string)
			if e != nil || !valid || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if e = walkJSON(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e := walkJSON(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func mutationID(w http.ResponseWriter, r *http.Request, body []byte) (Idempotency, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || len(values[0]) < 8 || len(values[0]) > 255 || strings.TrimSpace(values[0]) != values[0] {
		writeProblem(w, 400, "invalid_idempotency_key")
		return Idempotency{}, false
	}
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.Query().Encode()
	}
	return Idempotency{Key: values[0], Fingerprint: sha256.Sum256([]byte(r.Method + "\n" + target + "\n" + string(body)))}, true
}
func parsePage(r *http.Request) (string, int, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", 0, false
	}
	for key, v := range q {
		if (key != "cursor" && key != "limit") || len(v) != 1 {
			return "", 0, false
		}
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return "", 0, false
		}
	}
	return q.Get("cursor"), limit, true
}
func (s *Server) respond(w http.ResponseWriter, status int, value any, replay bool, err error) {
	if err != nil {
		writeProblem(w, statusFor(err), codeFor(err))
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request, _ []byte) {
	v, e := s.service.ListRoles(r.Context(), principal(r))
	s.respond(w, 200, map[string]any{"data": v}, false, e)
}
func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, _ []byte) {
	c, l, ok := parsePage(r)
	if !ok {
		writeProblem(w, 400, "invalid_request")
		return
	}
	v, e := s.service.ListMembers(r.Context(), principal(r), c, l)
	s.respond(w, 200, v, false, e)
}
func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request, _ []byte) {
	c, l, ok := parsePage(r)
	if !ok {
		writeProblem(w, 400, "invalid_request")
		return
	}
	v, e := s.service.ListInvitations(r.Context(), principal(r), c, l)
	s.respond(w, 200, v, false, e)
}
func (s *Server) listActions(w http.ResponseWriter, r *http.Request, _ []byte) {
	c, l, ok := parsePage(r)
	if !ok {
		writeProblem(w, 400, "invalid_request")
		return
	}
	v, e := s.service.ListSecurityActions(r.Context(), principal(r), c, l)
	s.respond(w, 200, v, false, e)
}
func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request, b []byte) {
	var in CreateInvitationInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.CreateInvitation(r.Context(), principal(r), in, id)
	s.respond(w, 201, v, replay, e)
}
func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request, b []byte) {
	var in InvitationDecision
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.RevokeInvitation(r.Context(), principal(r), r.PathValue("id"), in, id)
	s.respond(w, 200, v, replay, e)
}
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request, b []byte) {
	var in AcceptInvitationInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.AcceptInvitation(r.Context(), principal(r), in, id)
	s.respond(w, 200, v, replay, e)
}
func (s *Server) replaceRoles(w http.ResponseWriter, r *http.Request, b []byte) {
	var in RoleChangeInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.ReplaceRoles(r.Context(), principal(r), r.PathValue("id"), in, id)
	s.respond(w, 200, v, replay, e)
}
func (s *Server) memberMutation(w http.ResponseWriter, r *http.Request, b []byte, op string) {
	var in MemberMutationInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.MutateMember(r.Context(), principal(r), r.PathValue("id"), op, in, id)
	s.respond(w, 200, v, replay, e)
}
func (s *Server) disableMember(w http.ResponseWriter, r *http.Request, b []byte) {
	s.memberMutation(w, r, b, "disable")
}
func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, b []byte) {
	s.memberMutation(w, r, b, "remove")
}
func (s *Server) createAction(w http.ResponseWriter, r *http.Request, b []byte) {
	var in CreateSecurityActionInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.CreateSecurityAction(r.Context(), principal(r), in, id)
	s.respond(w, 201, v, replay, e)
}
func (s *Server) actionDecision(w http.ResponseWriter, r *http.Request, b []byte, approve bool) {
	var in SecurityDecisionInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.DecideSecurityAction(r.Context(), principal(r), r.PathValue("id"), approve, in, id)
	s.respond(w, 200, v, replay, e)
}
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request, b []byte) {
	s.actionDecision(w, r, b, true)
}
func (s *Server) rejectAction(w http.ResponseWriter, r *http.Request, b []byte) {
	s.actionDecision(w, r, b, false)
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, _ []byte) {
	v, e := s.service.GetSettings(r.Context(), principal(r))
	s.respond(w, 200, v, false, e)
}
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request, b []byte) {
	var in UpdateSettingsInput
	if !decode(b, &in) {
		writeProblem(w, 400, "invalid_request")
		return
	}
	id, ok := mutationID(w, r, b)
	if !ok {
		return
	}
	v, replay, e := s.service.UpdateSettings(r.Context(), principal(r), in, id)
	s.respond(w, 200, v, replay, e)
}

func writeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code})
}
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return 401
	case errors.Is(err, ErrForbidden):
		return 403
	case errors.Is(err, ErrNotFound):
		return 404
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrApprovalRequired):
		return 409
	case errors.Is(err, ErrInvalid):
		return 400
	case errors.Is(err, ErrDependency):
		return 503
	default:
		return 500
	}
}
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return "authentication_required"
	case errors.Is(err, ErrForbidden):
		return "mfa_or_permission_required"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, ErrApprovalRequired):
		return "security_approval_required"
	case errors.Is(err, ErrConflict):
		return "state_conflict"
	case errors.Is(err, ErrInvalid):
		return "invalid_request"
	case errors.Is(err, ErrDependency):
		return "dependency_unavailable"
	default:
		return "internal_error"
	}
}
