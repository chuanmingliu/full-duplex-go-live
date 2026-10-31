package telephony

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	c := testConfig(t)
	b, _ := json.Marshal(c)
	loaded, e := LoadConfig(strings.NewReader(string(b)))
	if e != nil {
		t.Fatal(e)
	}
	if loaded.RingTimeout != 30*time.Second || loaded.MaxDuration != 600*time.Second {
		t.Fatal("duration defaults")
	}
	for _, suffix := range []string{`,"typo":true}`, `,"ring_timeout_seconds":999999999}`, `,"sip_password":"secret"}`} {
		input := strings.TrimSuffix(string(b), "}") + suffix
		if _, e := LoadConfig(strings.NewReader(input)); e == nil {
			t.Fatal("accepted invalid config")
		}
	}
	if _, e := LoadConfig(strings.NewReader(string(b) + " {}")); e == nil {
		t.Fatal("accepted two JSON objects")
	}
}

func TestConfigRestrictions(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"missing media allowlist": func(c *Config) { c.MediaNetworks = nil },
		"header injection":        func(c *Config) { c.AllowedURI = "sip:a@127.0.0.1\r\nVia: bad" },
		"bad codec":               func(c *Config) { c.Codec = "opus" },
		"fragment":                func(c *Config) { c.GoliveURL = "http://127.0.0.1:9875/#bad" },
		"unspecified pbx":         func(c *Config) { c.PBX = "0.0.0.0:5060" },
		"unspecified advert":      func(c *Config) { c.AdvertiseIP = "0.0.0.0" },
		"unbounded queue":         func(c *Config) { c.QueueMS = 9999 },
	} {
		t.Run(name, func(t *testing.T) {
			c := testConfig(t)
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestWrongAgentFormatNeverDials(t *testing.T) {
	c := testConfig(t)
	pbx := udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	a := newFakeAgent(t, "pcm16")
	c.GoliveURL = a.server.URL
	if _, e := Run(context.Background(), c, Call{ID: "bad-format"}); e == nil {
		t.Fatal("accepted PCM16 from G.711 session")
	}
	_ = pbx.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	b := make([]byte, 2048)
	if _, _, e := pbx.ReadFromUDP(b); e == nil {
		t.Fatal("dialed before verifying agent format")
	}
}

func TestRingTimeoutCancels(t *testing.T) {
	c := testConfig(t)
	c.RingTimeout = time.Second
	pbx := udpSocket(t)
	c.PBX = pbx.LocalAddr().String()
	a := newFakeAgent(t, c.wireFormat())
	c.GoliveURL = a.server.URL
	done := make(chan error, 1)
	go func() { _, e := Run(context.Background(), c, Call{ID: "ring-timeout"}); done <- e }()
	invite, peer := expectMethod(t, pbx, "INVITE")
	sipReply(t, pbx, peer, invite, 180, "", "")
	cancel, p := expectMethod(t, pbx, "CANCEL")
	sipReply(t, pbx, p, cancel, 200, "", "")
	sipReply(t, pbx, peer, invite, 487, "", "")
	expectMethod(t, pbx, "ACK")
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("timeout counted as success")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancel did not finish")
	}
	select {
	case <-a.opened:
		t.Fatal("opened timed-out call")
	default:
	}
}
