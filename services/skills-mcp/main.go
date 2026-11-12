package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed panel/*
var panel embed.FS

func secret(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	v := strings.TrimSpace(string(b))
	if len(v) < 16 {
		log.Fatalf("invalid secret file %s", path)
	}
	return v
}

func protect(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "Bearer credential required", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		h.ServeHTTP(w, r)
	})
}
func (s *store) admin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		s.mu.RLock()
		c := s.catalog
		s.mu.RUnlock()
		json.NewEncoder(w).Encode(c)
	case "PUT":
		var c Catalog
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid catalog JSON", 400)
			return
		}
		c, err := s.publish(c)
		if err != nil {
			var pe *os.PathError
			switch {
			case errors.Is(err, errConflict):
				http.Error(w, err.Error(), 409)
			case errors.As(err, &pe):
				http.Error(w, "could not persist catalog", 500)
			default:
				http.Error(w, err.Error(), 400)
			}
			return
		}
		json.NewEncoder(w).Encode(c)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "unsupported method", 405)
	}
}
func handler(s *store, adminToken, runtimeToken string, memory ...*memoryAPI) http.Handler {
	mux := http.NewServeMux()
	if len(memory) > 0 && memory[0] != nil {
		mux.Handle("/admin/memory/", protect(adminToken, memory[0].handler()))
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { s.mu.RLock(); defer s.mu.RUnlock(); return s.server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux.Handle("/mcp", protect(runtimeToken, h))
	mux.Handle("POST /admin/preview", protect(adminToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var turn turnInput
		if err := json.NewDecoder(r.Body).Decode(&turn); err != nil {
			http.Error(w, "invalid event or state", 400)
			return
		}
		s.mu.RLock()
		c := s.catalog
		s.mu.RUnlock()
		skills, tools, err := c.selectTurn(turn)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"revision": c.Revision, "skills": skills, "tools": tools, "prompt_rendering": PromptRendering{Version: c.renderingVersion()}})
	})))
	mux.Handle("/admin/catalog", protect(adminToken, http.HandlerFunc(s.admin)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ready\n") })
	assets, _ := fs.Sub(panel, "panel")
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'none'")
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "untrusted origin", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func main() {
	const addr = "127.0.0.1:8794"
	if len(os.Args) == 2 && os.Args[1] == "health" {
		c := http.Client{Timeout: 2 * time.Second}
		r, e := c.Get("http://" + addr + "/healthz")
		if e != nil || r.StatusCode != 200 {
			os.Exit(1)
		}
		r.Body.Close()
		return
	}
	adminToken, runtimeToken := secret("/run/secrets/admin"), secret("/run/secrets/runtime")
	if adminToken == runtimeToken {
		log.Fatal("admin and runtime credentials must differ")
	}
	b := backend{"http://127.0.0.1:8791/api/tools/quote", secret("/run/secrets/crm"), &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s, err := openStore("/data/catalog.json", b)
	if err != nil {
		log.Fatal(err)
	}
	memory, closeMemory, err := startMemory(s)
	if err != nil {
		log.Fatal(err)
	}
	defer closeMemory()
	srv := &http.Server{Addr: addr, Handler: handler(s, adminToken, runtimeToken, memory), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 200 * time.Second, IdleTimeout: 45 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
