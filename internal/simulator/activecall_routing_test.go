package simulator

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newRoutingActiveCall(t *testing.T, controlURL string) *ActiveCall {
	t.Helper()
	a, err := NewActiveCall(ActiveCallOptions{
		ControlURL: controlURL, RealtimeURL: "ws://simulator:18891/v1/realtime",
		WebhookToken: "webhook-secret", MediaToken: strings.Repeat("x", 48),
		Destination: "simulator-receiver", GatewayIdentity: "isolated-active-call", SIPDomain: "simulator.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestActiveCallRunFromGatewayHostnameAndIPv6(t *testing.T) {
	for _, tc := range []struct {
		controlURL string
		hosts      []string
	}{
		{"http://activecall:8080", []string{"activecall", "ACTIVECall:25060", "activecall.:25060"}},
		{"http://[fd00::2]:8080", []string{"[fd00::2]", "[fd00::2]:25060", "[fd00:0:0:0:0:0:0:2]:25060"}},
		{"http://172.20.0.2:8080", []string{"[::ffff:172.20.0.2]:25060"}},
	} {
		t.Run(tc.controlURL, func(t *testing.T) {
			a := newRoutingActiveCall(t, tc.controlURL)
			a.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
				t.Fatal("configured hostname or direct IP must not require DNS")
				return nil, nil
			}
			for _, host := range tc.hosts {
				if got := a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@"+host); got != activeCallTestRun {
					t.Fatalf("host %q produced run %q", host, got)
				}
			}
		})
	}
}

func TestActiveCallRunFromResolvedGatewayAddress(t *testing.T) {
	a := newRoutingActiveCall(t, "http://activecall:8080")
	lookups := 0
	a.lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		if host != "activecall" {
			t.Fatalf("resolved untrusted name %q", host)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > gatewayLookupTimeout {
			t.Fatal("gateway resolution has no bounded deadline")
		}
		return []net.IPAddr{{IP: net.ParseIP("172.20.0.2")}, {IP: net.ParseIP("fd00::2")}}, nil
	}
	for _, host := range []string{"172.20.0.2", "172.20.0.2:25060", "[fd00::2]", "[fd00::2]:25060"} {
		if got := a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@"+host); got != activeCallTestRun {
			t.Fatalf("resolved gateway host %q produced run %q", host, got)
		}
	}
	for _, host := range []string{"172.20.0.1:25060", "192.168.71.9", "[fd00::3]:25060"} {
		if got := a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@"+host); got != "" {
			t.Fatalf("unrelated address %q produced run %q", host, got)
		}
	}
	before := lookups
	for _, host := range []string{"evil.invalid", "activecall.evil.invalid", "172.20.0.2.evil.invalid", "activecall:invalid"} {
		if got := a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@"+host); got != "" {
			t.Fatalf("unrelated hostname %q produced run %q", host, got)
		}
	}
	if lookups != before {
		t.Fatal("untrusted or malformed hostname triggered DNS")
	}
}

func TestActiveCallGatewayLookupRecoversAndDoesNotRetainStaleAddress(t *testing.T) {
	a := newRoutingActiveCall(t, "http://activecall:8080")
	ip := ""
	a.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		if ip == "" {
			return nil, errors.New("gateway DNS not ready")
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
	callee := "sip:" + activeCallTestRun + "@172.20.0.2:25060"
	if a.runFromCallee(context.Background(), callee) != "" {
		t.Fatal("unresolved address accepted")
	}
	ip = "172.20.0.2"
	if a.runFromCallee(context.Background(), callee) != activeCallTestRun {
		t.Fatal("gateway could not become ready after startup")
	}
	ip = "172.20.0.3"
	if a.runFromCallee(context.Background(), callee) != "" {
		t.Fatal("previous gateway address remained trusted after replacement")
	}
	if a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@172.20.0.3") != activeCallTestRun {
		t.Fatal("replacement gateway address rejected")
	}
}

func TestActiveCallGatewayLookupHonorsRequestDeadlineOutsideMutex(t *testing.T) {
	a := newRoutingActiveCall(t, "http://activecall:8080")
	a.SetController(&activeCallTestController{})
	a.lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if !a.mu.TryLock() {
			t.Error("DNS resolution held adapter mutex")
		} else {
			a.mu.Unlock()
		}
		<-ctx.Done()
		// Even a resolver returning data after cancellation cannot admit the URI.
		return []net.IPAddr{{IP: net.ParseIP("172.20.0.2")}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, call, _, err := a.register(ctx, activeCallInvite{DialogID: "deadline", Callee: "sip:" + activeCallTestRun + "@172.20.0.2:25060"})
	if err != nil || call.runID != "" {
		t.Fatalf("cancelled lookup admitted binding: call=%+v err=%v", call, err)
	}
}

func TestActiveCallGatewayLookupRequiresAuthenticatedWebhook(t *testing.T) {
	a := newRoutingActiveCall(t, "http://activecall:8080")
	a.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("unauthenticated request triggered DNS")
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/active-call/incoming", strings.NewReader(`{"event":"invite","dialogId":"forged","callee":"sip:`+activeCallTestRun+`@172.20.0.2:25060"}`))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || len(a.calls) != 0 {
		t.Fatal("unauthenticated invite created a call")
	}
}

func TestActiveCallRunFromForwardedGatewayURI(t *testing.T) {
	a := newRoutingActiveCall(t, "http://172.20.0.2:8080")
	for _, host := range []string{"simulator.invalid", "SIMULATOR.INVALID", "172.20.0.2", "172.20.0.2:25060"} {
		t.Run(host, func(t *testing.T) {
			if got := a.runFromCallee(context.Background(), "sip:"+activeCallTestRun+"@"+host); got != activeCallTestRun {
				t.Fatalf("forwarded host %q: got run %q, want %q", host, got, activeCallTestRun)
			}
		})
	}
}

func TestActiveCallRunFromCalleeRejectsUntrustedOrMalformedURI(t *testing.T) {
	a := newRoutingActiveCall(t, "http://172.20.0.2:8080")
	for _, callee := range []string{
		"sip:" + activeCallTestRun + "@172.20.0.1:25060",
		"sip:" + activeCallTestRun + "@192.168.71.9:25060",
		"sip:" + activeCallTestRun + "@wrong.invalid",
		"sip:" + activeCallTestRun + "@172.20.0.2.evil.invalid",
		"sip:" + activeCallTestRun + "@simulator.invalid:25060",
		"sip:" + activeCallTestRun + "@172.20.0.2:0",
		"sip:" + activeCallTestRun + "@172.20.0.2:65536",
		"sip:" + activeCallTestRun + "@172.20.0.2:port",
		"sip:" + activeCallTestRun + "@172.20.0.2:",
		"sip:" + activeCallTestRun + "@172.20.0.2;transport=udp",
		"sip:" + activeCallTestRun + "@172.20.0.2?run=latest",
		"sip:" + activeCallTestRun + "@172.20.0.2#fragment",
		"sip:" + activeCallTestRun + "@172.20.0.2/",
		"sip:" + activeCallTestRun + "@172.20.0.2@simulator.invalid",
		"sip:" + activeCallTestRun + "%40simulator.invalid@172.20.0.2",
		"sip:" + activeCallTestRun + "@%31%37%32.20.0.2",
		"sip:" + activeCallTestRun + "@ 172.20.0.2",
		"sip:" + activeCallTestRun + "@172.20.0.2\r\n",
		"sip:fixed-number@172.20.0.2",
		"sip:sim-FFFFFFFFFFFFFFFFFFFFFFFF@172.20.0.2",
		"sip:sim-0123456789abcdef0123456@172.20.0.2",
		"sips:" + activeCallTestRun + "@172.20.0.2",
		"<sip:" + activeCallTestRun + "@172.20.0.2>",
		"sip:" + activeCallTestRun + "@[172.20.0.2]:25060",
		"sip:" + activeCallTestRun + "@fd00::2",
		"sip:" + activeCallTestRun + "@[fd00::2",
		"sip:" + activeCallTestRun + "@[fd00::2]:25060:1",
		"sip:" + activeCallTestRun + "@[fd00::2%eth0]:25060",
		"sip:" + activeCallTestRun + "@[fd00::2]suffix",
	} {
		t.Run(callee, func(t *testing.T) {
			if got := a.runFromCallee(context.Background(), callee); got != "" {
				t.Fatalf("untrusted or malformed URI resolved to %q", got)
			}
		})
	}
}
