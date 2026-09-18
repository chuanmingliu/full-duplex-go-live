package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConversationalExtrasAreOffByDefault pins the three behaviours that make
// the agent speak when it was not asked something.
//
// Each was built to make delegation feel less like a half-duplex cascade, and
// each was heard as the opposite. An acknowledgement lands on top of the
// caller; a filler is heard as the agent having nothing to say, and on the
// first turn of a call it is the first thing the caller hears from it; the
// floor hold delays a genuine interruption by however long the recognizer takes
// to disagree. They stay in the code, switchable in one boolean, and stay off
// until someone asks for them.
func TestConversationalExtrasAreOffByDefault(t *testing.T) {
	d := Default().Duplex

	if d.Backchannel {
		t.Error("backchannel is on by default; the assistant would speak while the caller is talking")
	}
	if d.HoldingFiller {
		t.Error("holding_filler is on by default; the assistant would cover backend waits unasked")
	}
	if len(d.UserBackchannelPhrases) != 0 {
		t.Errorf("user_backchannel_phrases has %d entries by default; deciding that a word "+
			"the caller said is not an interruption is opt-in", len(d.UserBackchannelPhrases))
	}

	// The lists for the two switched-off behaviours stay populated: turning one
	// back on must not mean retyping what to say.
	if len(d.BackchannelPhrases) == 0 || len(d.HoldingFillerPhrases) == 0 {
		t.Error("a phrase list was emptied along with its switch; turning the behaviour " +
			"back on should be one boolean")
	}

	// The floor hold is the exception, and stays on. It arrived with the phrase
	// list above but is not the same feature: what it does is wait for the
	// transcript before yielding, and the case it was written for has no phrase
	// list in it — a cough, or the assistant's own voice through a
	// speakerphone, opening the VAD on a greeting that the recognizer then
	// finds no words in. Without it the greeting is cut mid-name.
	if d.UserBackchannelHoldMS <= 0 {
		t.Error("user_backchannel_hold_ms is off by default; a wordless noise would cut " +
			"the assistant off mid-sentence")
	}
}

func TestAuthTokenIsNeverLoadedFromAProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	if err := os.WriteFile(path, []byte(`{"auth_token":"leaked","log_level":"warn"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthToken != "" {
		t.Fatal("auth_token in a profile was loaded; credentials must stay in the environment")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("log_level = %q; the rest of the profile should still apply", cfg.LogLevel)
	}
}

func TestAuthRequiredWithoutTokenFailsClosed(t *testing.T) {
	t.Setenv("GOLIVE_AUTH_TOKEN", "")
	cfg := Default()
	cfg.AuthRequired = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("auth_required with an empty token must not validate")
	}
}

func TestAuthTokenFromEnv(t *testing.T) {
	t.Setenv("GOLIVE_AUTH_TOKEN", "from-env")
	t.Setenv("GOLIVE_AUTH_REQUIRED", "true")
	t.Setenv("GOLIVE_MAX_SESSIONS", "8")
	t.Setenv("GOLIVE_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthToken != "from-env" {
		t.Errorf("token = %q", cfg.AuthToken)
	}
	if !cfg.AuthRequired {
		t.Error("auth_required not set from env")
	}
	if cfg.MaxSessions != 8 {
		t.Errorf("max_sessions = %d", cfg.MaxSessions)
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Errorf("origins = %v", cfg.AllowedOrigins)
	}
}

func TestMetricsTokenIsNeverLoadedFromAProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	if err := os.WriteFile(path, []byte(`{"metrics_token":"leaked","metrics_public":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsToken != "" {
		t.Fatal("metrics_token in a profile was loaded")
	}
	if !cfg.MetricsPublic {
		t.Error("metrics_public should still apply")
	}
}
