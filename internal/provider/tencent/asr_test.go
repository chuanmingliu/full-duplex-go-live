package tencent

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// The signature is the one thing in this adapter that cannot be discovered by
// trial and error against a live endpoint without burning quota on 4xx
// responses, so it is pinned here.
func TestSignedURLSignsUnescapedParameters(t *testing.T) {
	a := &ASR{
		AppID:     "1250000000",
		SecretID:  "AKIDtestsecretid",
		SecretKey: "testsecretkey",
		Engine:    "16k_zh",
	}
	now := time.Unix(1_700_000_000, 0)

	raw := a.SignedURL("voice-123", now)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("SignedURL produced an unparseable URL: %v", err)
	}
	if parsed.Scheme != "wss" || parsed.Host != "asr.cloud.tencent.com" {
		t.Errorf("unexpected endpoint %s://%s", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/asr/v2/1250000000" {
		t.Errorf("path is %q, want /asr/v2/1250000000", parsed.Path)
	}

	query := parsed.Query()
	signature := query.Get("signature")
	if signature == "" {
		t.Fatal("no signature in the URL")
	}

	// Recompute independently: sort every parameter except the signature, join
	// them with their *unescaped* values, and HMAC-SHA1 with the secret key.
	keys := make([]string, 0, len(query))
	for k := range query {
		if k != "signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+query.Get(k))
	}
	source := "asr.cloud.tencent.com/asr/v2/1250000000?" + strings.Join(pairs, "&")

	mac := hmac.New(sha1.New, []byte(a.SecretKey))
	mac.Write([]byte(source))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if signature != want {
		t.Errorf("signature mismatch\n got: %s\nwant: %s\nsigned source: %s", signature, want, source)
	}
}

func TestSignedURLDisablesProviderVAD(t *testing.T) {
	a := &ASR{AppID: "1", SecretID: "id", SecretKey: "key", Engine: "16k_zh"}
	q, err := url.Parse(a.SignedURL("v", time.Unix(1_700_000_000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	values := q.Query()

	// golive owns turn segmentation. Letting Tencent segment as well produces
	// two disagreeing notions of where an utterance ends.
	if got := values.Get("needvad"); got != "0" {
		t.Errorf("needvad is %q, want \"0\"", got)
	}
	if got := values.Get("voice_format"); got != "1" {
		t.Errorf("voice_format is %q, want \"1\" (raw PCM)", got)
	}
	if got := values.Get("engine_model_type"); got != "16k_zh" {
		t.Errorf("engine_model_type is %q", got)
	}
	if values.Get("expired") == "" || values.Get("nonce") == "" || values.Get("timestamp") == "" {
		t.Error("expired/nonce/timestamp must all be present")
	}
}
