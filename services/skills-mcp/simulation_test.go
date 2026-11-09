package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Runs the real MCP implementation with public synthetic catalog contents and
// an unreachable business backend. Used only by the opt-in box comparison.
func TestSkillsSimulationService(t *testing.T) {
	root := os.Getenv("SKILLS_FIXTURE_DIR")
	if root == "" {
		t.Skip("opt-in isolated simulation service")
	}
	s, err := openStore(filepath.Join(root, "catalog.json"), backend{"http://127.0.0.1:1", "simulation-crm-token", http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18795")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: handler(s, "simulation-admin-token", "simulation-runtime-token"), ReadHeaderTimeout: 5 * time.Second}
	t.Log("isolated MCP simulation ready")
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
