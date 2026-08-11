package matchingadmin

import (
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
	server := &Server{service: service, authenticator: authenticator, mux: http.NewServeMux(), bodyLimit: bodyLimit}
	server.mux.HandleFunc("POST /v1/management/matching-policies", server.authenticated(server.create))
	server.mux.HandleFunc("GET /v1/management/matching-policies", server.authenticated(server.list))
	server.mux.HandleFunc("GET /v1/management/matching-policies/{id}", server.authenticated(server.get))
	server.mux.HandleFunc("POST /v1/management/matching-policies/{id}/request-approval", server.authenticated(server.requestApproval))
	server.mux.HandleFunc("POST /v1/management/matching-policies/{id}/approve", server.authenticated(server.approve))
	server.mux.HandleFunc("POST /v1/management/matching-policies/{id}/activate", server.authenticated(server.activate))
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

type handler func(http.ResponseWriter, *http.Request, []byte)

func (s *Server) authenticated(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := s.readBody(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		principal, err := s.authenticator.Authenticate(r.Context(), r, body)
		if err != nil {
			s.problem(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		r = r.WithContext(withPrincipal(r.Context(), principal))
		next(w, r, body)
	}
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, body []byte) {
	var input PolicyInput
	if !decodeStrict(body, &input) {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	idem, ok := policyIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.Create(r.Context(), principalFrom(r.Context()), input, idem)
	s.respond(w, http.StatusCreated, result, replay, err)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, _ []byte) {
	result, err := s.service.Get(r.Context(), principalFrom(r.Context()), r.PathValue("id"))
	s.respond(w, http.StatusOK, result, false, err)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, _ []byte) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	for key, values := range query {
		if key != "cursor" && key != "limit" || len(values) != 1 {
			s.problem(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			s.problem(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	result, err := s.service.List(r.Context(), principalFrom(r.Context()), query.Get("cursor"), limit)
	s.respond(w, http.StatusOK, result, false, err)
}

func (s *Server) requestApproval(w http.ResponseWriter, r *http.Request, body []byte) {
	var input Mutation
	if !decodeStrict(body, &input) {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	idem, ok := policyIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.RequestApproval(r.Context(), principalFrom(r.Context()), r.PathValue("id"), input, idem)
	s.respond(w, http.StatusOK, result, replay, err)
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request, body []byte) {
	var input Mutation
	if !decodeStrict(body, &input) {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	idem, ok := policyIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.Approve(r.Context(), principalFrom(r.Context()), r.PathValue("id"), input, idem)
	s.respond(w, http.StatusOK, result, replay, err)
}

func (s *Server) activate(w http.ResponseWriter, r *http.Request, body []byte) {
	var input Activation
	if !decodeStrict(body, &input) {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return
	}
	idem, ok := policyIdempotency(w, r, body)
	if !ok {
		return
	}
	result, replay, err := s.service.Activate(r.Context(), principalFrom(r.Context()), r.PathValue("id"), input, idem)
	s.respond(w, http.StatusOK, result, replay, err)
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request, required bool) ([]byte, bool) {
	if !required {
		return nil, true
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || strings.ToLower(mediaType) != "application/json" || len(parameters) != 0 {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.bodyLimit))
	if err != nil || len(body) == 0 || !uniqueJSON(body) {
		s.problem(w, http.StatusBadRequest, "invalid_request")
		return nil, false
	}
	return body, true
}

func policyIdempotency(w http.ResponseWriter, r *http.Request, body []byte) (Idempotency, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || len(values[0]) < 8 || len(values[0]) > 255 || strings.TrimSpace(values[0]) != values[0] {
		writePolicyProblem(w, http.StatusBadRequest, "invalid_idempotency_key")
		return Idempotency{}, false
	}
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.Query().Encode()
	}
	hash := sha256.Sum256([]byte(r.Method + "\n" + target + "\n" + string(body)))
	return Idempotency{Key: values[0], Fingerprint: hash}, true
}

func decodeStrict(body []byte, target any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func uniqueJSON(body []byte) bool {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if uniqueValue(decoder) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func uniqueValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
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
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if err := uniqueValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := uniqueValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return ErrInvalid
	}
}

func (s *Server) respond(w http.ResponseWriter, status int, value any, replay bool, err error) {
	if err != nil {
		s.problem(w, statusFor(err), codeFor(err))
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

func (s *Server) problem(w http.ResponseWriter, status int, code string) {
	writePolicyProblem(w, status, code)
}

func writePolicyProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, ErrIdempotency):
		return http.StatusConflict
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
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
	case errors.Is(err, ErrIdempotency):
		return "idempotency_conflict"
	case errors.Is(err, ErrConflict):
		return "state_conflict"
	case errors.Is(err, ErrInvalid):
		return "invalid_request"
	default:
		return "internal_error"
	}
}
