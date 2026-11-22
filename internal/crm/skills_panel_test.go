package crm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLeadsSkillsPanelAuthorization(t *testing.T) {
	const leads = "fixture-leads-token"
	const admin = "fixture-skills-admin-token"
	file := filepath.Join(t.TempDir(), "skills-admin")
	if err := os.WriteFile(file, []byte(admin+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var adminRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if r.Header.Get("Authorization") != "" {
				t.Error("a credential was forwarded to a public asset")
			}
			return
		case "/admin/catalog", "/admin/preview":
			adminRequests.Add(1)
		default:
			t.Errorf("unexpected upstream route %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+admin || r.Header.Get("Cookie") != "" {
			t.Error("upstream received the wrong credential or a browser cookie")
		}
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"revision":7}` {
				t.Error("catalog update body changed")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=fixture")
		io.WriteString(w, `{"revision":7}`)
	}))
	defer upstream.Close()
	mux := http.NewServeMux()
	s := &Service{cfg: Config{SkillsAdminTokenFile: file}, token: leads}
	s.registerSkillsPanel(mux, upstream.URL)
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/api/skills-tools/access", "", 401},
		{"GET", "/api/skills-tools/access", leads, 200},
		{"GET", "/skills-tools/", "", 200},
		{"GET", "/skills-tools/admin/catalog", admin, 401},
		{"PUT", "/skills-tools/admin/catalog", "", 401},
		{"GET", "/skills-tools/admin/catalog", leads, 200},
		{"PUT", "/skills-tools/admin/catalog", leads, 200},
		{"POST", "/skills-tools/admin/preview", leads, 200},
		{"POST", "/skills-tools/mcp", leads, 404},
		{"GET", "/skills-tools/admin/memory/", leads, 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"revision":7}`))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Cookie", "crm=fixture")
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != tc.status || out.Header().Get("Set-Cookie") != "" || strings.Contains(out.Body.String(), admin) {
			t.Fatalf("%s %s = %d, want %d with credentials kept private", tc.method, tc.path, out.Code, tc.status)
		}
	}
	if got := adminRequests.Load(); got != 3 {
		t.Fatalf("upstream received %d admin requests, want 3", got)
	}
}
