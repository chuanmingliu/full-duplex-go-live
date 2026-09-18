package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"

	"github.com/chuanmingliu/golive/internal/metrics"
)

type ctxKey int

const requestIDKey ctxKey = 1

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// withHTTP wraps every route with a request id, panic recover, security
// headers and an access log that never prints a ?token=. WebSocket upgrades
// hijack the connection; the wrapper implements http.Hijacker so they still
// work, and recover refuses to write a 500 onto a hijacked socket.
func (s *Server) withHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = randomID()
		}
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))

		cap := &capture{ResponseWriter: w, status: 0}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("http panic", "err", rec, "path", redactURL(r.URL), "request_id", id)
				metrics.Default().Error("panic")
				if cap.status == 0 {
					// Nested recover: writing after a hijack panics again.
					func() {
						defer func() { _ = recover() }()
						http.Error(cap, "internal error", http.StatusInternalServerError)
					}()
				}
			}
			s.logHTTP(r, cap.status, time.Since(start), id)
		}()
		next.ServeHTTP(cap, r)
	})
}

func (s *Server) logHTTP(r *http.Request, status int, d time.Duration, id string) {
	path := r.URL.Path
	quiet := path == "/livez" || path == "/healthz" || path == "/readyz" || path == "/metrics"
	if quiet && (status == 0 || status == http.StatusOK || status == http.StatusSwitchingProtocols) {
		s.log.Debug("http",
			"method", r.Method,
			"path", redactURL(r.URL),
			"status", status,
			"ms", d.Milliseconds(),
			"ip", s.clientIP(r),
			"request_id", id)
		return
	}
	s.log.Info("http",
		"method", r.Method,
		"path", redactURL(r.URL),
		"status", status,
		"ms", d.Milliseconds(),
		"ip", s.clientIP(r),
		"request_id", id)
}

// capture records the status code and forwards hijacks to the underlying
// ResponseWriter, which gorilla/websocket needs.
type capture struct {
	http.ResponseWriter
	status int
}

func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.ResponseWriter.Write(b)
}

func (c *capture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errNoHijack
	}
	if c.status == 0 {
		c.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

var errNoHijack = errString("http: response does not support hijack")

type errString string

func (e errString) Error() string { return string(e) }
