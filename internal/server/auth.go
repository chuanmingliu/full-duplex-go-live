package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/metrics"
)

var errUnauthorized = errors.New("unauthorized")

// authorize checks the bearer token when the service has been given one, or
// when the profile requires auth. A missing token with auth_required is a
// start-up failure, not a request-time one — see config.Validate.
func (s *Server) authorize(r *http.Request) error {
	if s.cfg.AuthToken == "" && !s.cfg.AuthRequired {
		return nil
	}
	got := bearerToken(r)
	if got == "" {
		return errUnauthorized
	}
	if !tokenEqual(got, s.cfg.AuthToken) {
		return errUnauthorized
	}
	return nil
}

// authorizeMetrics is the scrape-side check. A dedicated token lets Prometheus
// scrape without holding the session secret; metrics_public is for a private
// network where even that is more trouble than it is worth.
func (s *Server) authorizeMetrics(r *http.Request) error {
	if s.cfg.MetricsPublic {
		return nil
	}
	got := bearerToken(r)
	if s.cfg.MetricsToken != "" {
		if tokenEqual(got, s.cfg.MetricsToken) {
			return nil
		}
		if s.cfg.AuthToken != "" && tokenEqual(got, s.cfg.AuthToken) {
			return nil
		}
		return errUnauthorized
	}
	return s.authorize(r)
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
	}
	// Browsers cannot set WebSocket headers, so the demo (and any JS client)
	// passes the token as a query parameter. Non-browser clients should prefer
	// the header, which does not end up in access logs of a well-configured
	// proxy.
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	return ""
}

// tokenEqual compares credentials in constant time even when the lengths
// differ. crypto/subtle.ConstantTimeCompare panics on unequal lengths, which
// would turn a wrong password into a 500 and a timing oracle into a crash.
func tokenEqual(got, want string) bool {
	gh := sha256.Sum256([]byte(got))
	wh := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (golivectl, a SIP gateway) send no Origin.
		return true
	}
	if allowed := s.cfg.AllowedOrigins; len(allowed) > 0 {
		for _, a := range allowed {
			if a == "*" || a == origin {
				return true
			}
		}
		return false
	}
	// No allow-list. In demo mode (no auth) this is the historical "allow
	// anything" behaviour the browser page needs. Once a token is configured
	// the default tightens to same-host, because a credentialed socket from
	// an arbitrary origin is a CSRF hole.
	if s.cfg.AuthToken == "" && !s.cfg.AuthRequired {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorize(r); err != nil {
			s.rejectAuth(w, r)
			return
		}
		next(w, r)
	}
}

func (s *Server) protectMetrics(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorizeMetrics(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Server) rejectAuth(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.noteAuthFail(ip) {
		metrics.Default().Rejected("auth_throttled")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_requests"})
		return
	}
	metrics.Default().Rejected("unauthorized")
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
}

// AuthConfigured reports whether a request needs a token. Exported for tests.
func AuthConfigured(cfg config.Config) bool {
	return cfg.AuthToken != "" || cfg.AuthRequired
}

// redactURL strips credentials from a request URL so access logs and panic
// reports cannot leak a browser-supplied ?token=.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.Path
	}
	q := u.Query()
	if q.Has("token") {
		q.Set("token", "redacted")
	}
	return u.Path + "?" + q.Encode()
}

// failGate is a per-IP 401 budget. It exists to make token guessing expensive
// rather than to be a general rate limiter — session caps handle that.
type failGate struct {
	mu     sync.Mutex
	burst  int
	window time.Duration
	hits   map[string]failHits
}

type failHits struct {
	n    int
	from time.Time
}

func newFailGate(burst, windowSeconds int) *failGate {
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	return &failGate{
		burst:  burst,
		window: time.Duration(windowSeconds) * time.Second,
		hits:   map[string]failHits{},
	}
}

// noteAuthFail records a failed attempt. It returns false when the IP has
// exhausted its budget and should be answered 429 rather than 401.
func (s *Server) noteAuthFail(ip string) bool {
	g := s.authGate
	if g == nil || g.burst <= 0 {
		return true
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	h := g.hits[ip]
	if h.from.IsZero() || now.Sub(h.from) > g.window {
		h = failHits{from: now}
	}
	h.n++
	g.hits[ip] = h
	// Bound memory: a scan of the public internet should not grow this
	// without limit. Dropping the map is crude and correct — the budget
	// resets, which is the safe direction.
	if len(g.hits) > 8192 {
		g.hits = map[string]failHits{ip: h}
	}
	return h.n <= g.burst
}
