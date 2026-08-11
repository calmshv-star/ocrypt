package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

const (
	checkoutRateWindow = time.Minute
	checkoutRateLimit  = 120
	checkoutRateMaxIDs = 20_000
)

type rateWindow struct {
	started time.Time
	count   int
}

// checkoutRateLimiter is deliberately local and bounded. Deployments should
// additionally enforce a distributed/IP rate limit at the edge; this protects
// a single API process and prevents an unbounded token-key map.
type checkoutRateLimiter struct {
	mu      sync.Mutex
	windows map[[32]byte]rateWindow
	now     func() time.Time
}

func newCheckoutRateLimiter() *checkoutRateLimiter {
	return &checkoutRateLimiter{windows: make(map[[32]byte]rateWindow), now: time.Now}
}

func (l *checkoutRateLimiter) allow(remoteAddress, token string) bool {
	now := l.now().UTC()
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	key := sha256.Sum256([]byte(host + "\x00" + token))
	l.mu.Lock()
	defer l.mu.Unlock()
	window, ok := l.windows[key]
	if !ok || now.Sub(window.started) >= checkoutRateWindow {
		if len(l.windows) >= checkoutRateMaxIDs {
			for candidate, existing := range l.windows {
				if now.Sub(existing.started) >= checkoutRateWindow {
					delete(l.windows, candidate)
				}
			}
			if len(l.windows) >= checkoutRateMaxIDs {
				return false
			}
		}
		l.windows[key] = rateWindow{started: now, count: 1}
		return true
	}
	if window.count >= checkoutRateLimit {
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}

func (s *Server) getCheckoutSession(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !s.checkoutRate.allow(r.RemoteAddr, token) {
		w.Header().Set("Retry-After", "60")
		writeAPIError(w, http.StatusTooManyRequests, requestID(r), "rate_limited", "too many checkout requests")
		return
	}
	session, err := s.service.GetCheckoutSession(r.Context(), token)
	if err != nil {
		// The same generic response is used for malformed, unknown, revoked and
		// disabled-tenant tokens so this endpoint is not an identifier oracle.
		if errors.Is(err, domain.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, requestID(r), "not_found", "checkout session not found")
			return
		}
		writeError(w, requestID(r), err)
		return
	}
	body, err := json.Marshal(session)
	if err != nil {
		writeError(w, requestID(r), err)
		return
	}
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, candidate := range strings.Split(match, ",") {
			if strings.TrimSpace(candidate) == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	writeJSONStatus(w, http.StatusOK, session)
}
