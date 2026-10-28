package deepseek

import (
	"os"
	"testing"
)

// TestPerProviderOverridesWinOverTheSharedOnes pins the precedence that makes
// running two backends side by side actually work.
//
// The package has always claimed registering this twice is how you do that,
// and with only GOLIVE_BACKEND_* it was not true — the second provider
// inherited the first one's model id. Every existing .env has
// GOLIVE_BACKEND_MODEL set, so a shared variable that outranked the specific
// one would leave the new settings inert.
func TestPerProviderOverridesWinOverTheSharedOnes(t *testing.T) {
	t.Setenv("CEREBRAS_API_KEY", "test")
	t.Setenv("GOLIVE_BACKEND_BASE_URL", "https://api.deepseek.com")
	t.Setenv("GOLIVE_BACKEND_MODEL", "deepseek-chat")

	// Shared only: the backend settings apply, which is the old behaviour and
	// what keeps existing setups working.
	c, err := NewNamed("cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai", "qwen-3.8-27b")
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultModel != "deepseek-chat" || c.BaseURL != "https://api.deepseek.com" {
		t.Errorf("shared overrides ignored: base=%q model=%q", c.BaseURL, c.DefaultModel)
	}

	// Specific wins.
	t.Setenv("GOLIVE_CEREBRAS_MODEL", "google/gemma-4-31b-it")
	t.Setenv("GOLIVE_CEREBRAS_BASE_URL", "https://dedicated.example/v1")
	c, err = NewNamed("cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai", "qwen-3.8-27b")
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultModel != "google/gemma-4-31b-it" {
		t.Errorf("model = %q; GOLIVE_CEREBRAS_MODEL must outrank GOLIVE_BACKEND_MODEL", c.DefaultModel)
	}
	if c.BaseURL != "https://dedicated.example/v1" {
		t.Errorf("base = %q; GOLIVE_CEREBRAS_BASE_URL must outrank GOLIVE_BACKEND_BASE_URL", c.BaseURL)
	}

	// A different provider in the same process is unaffected by Cerebras's.
	t.Setenv("INCEPTION_API_KEY", "test")
	i, err := NewNamed("inception", "INCEPTION_API_KEY", "https://api.inceptionlabs.ai", "mercury-2.5")
	if err != nil {
		t.Fatal(err)
	}
	if i.DefaultModel == "google/gemma-4-31b-it" {
		t.Error("inception picked up Cerebras's model; the two providers are not isolated")
	}
	_ = os.Getenv
}

// TestEveryRegisteredBackendIsNamed guards the registration itself: a provider
// added without a name silently shares another's overrides.
func TestEveryRegisteredBackendIsNamed(t *testing.T) {
	t.Setenv("CEREBRAS_API_KEY", "test")
	t.Setenv("GOLIVE_CEREBRAS_MODEL", "sentinel")
	c, err := NewNamed("cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai", "qwen-3.8-27b")
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultModel != "sentinel" {
		t.Fatalf("model = %q, want the per-provider override", c.DefaultModel)
	}
	if c.Name() != "cerebras" {
		t.Errorf("Name() = %q, want cerebras (not a hard-coded deepseek)", c.Name())
	}
}

func TestCompletionsURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.deepseek.com":                          "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/":                         "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1":                       "https://api.deepseek.com/v1/chat/completions",
		"https://dashscope.aliyuncs.com/compatible-mode":    "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		"https://dashscope.aliyuncs.com/compatible-mode/v1": "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		"https://ark.cn-beijing.volces.com/api/v3":          "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		"https://open.bigmodel.cn/api/paas/v4/":             "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		"http://127.0.0.1:8000/v1/chat/completions":         "http://127.0.0.1:8000/v1/chat/completions",
		"http://vendor.example":                             "http://vendor.example/v1/chat/completions",
		"https://api.minimax.cn/v1/text/chatcompletion_v2":  "https://api.minimax.cn/v1/text/chatcompletion_v2",
		"https://api.minimax.cn/v1":                         "https://api.minimax.cn/v1/chat/completions",
	} {
		if got := completionsURL(base); got != want {
			t.Errorf("completionsURL(%q) = %q, want %q", base, got, want)
		}
	}
}
