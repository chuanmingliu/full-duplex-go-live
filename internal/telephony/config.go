// Package telephony implements the experimental outbound SIP/RTP transport.
// Conversation policy and speech providers remain in the golive server.
package telephony

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

// Config is private operator configuration, never supplied by a dial request.
type Config struct {
	// ContactUser separates dialog routing from the authenticated caller identity.
	ContactUser       string         `json:"contact_user,omitempty"`
	GoliveAudioFormat string         `json:"golive_audio_format,omitempty"`
	SIPListen         string         `json:"sip_listen"`
	AdvertiseIP       string         `json:"advertise_ip"`
	PBX               string         `json:"pbx"`
	AllowedURI        string         `json:"allowed_uri"`
	FromUser          string         `json:"from_user"`
	SIPUsername       string         `json:"sip_username"`
	SIPPassword       string         `json:"-"`
	GoliveToken       string         `json:"-"`
	GoliveURL         string         `json:"golive_url"`
	RecordDir         string         `json:"record_dir"`
	Codec             string         `json:"codec"`
	RTPMin            int            `json:"rtp_min"`
	RTPMax            int            `json:"rtp_max"`
	QueueMS           int            `json:"queue_ms"`
	MediaNetworks     []netip.Prefix `json:"media_networks"`
	RingTimeout       time.Duration  `json:"-"`
	MaxDuration       time.Duration  `json:"-"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (c Config) validate() error {
	listen, err := net.ResolveUDPAddr("udp4", c.SIPListen)
	host, _, _ := net.SplitHostPort(c.SIPListen)
	if err != nil || listen.IP.To4() == nil || net.ParseIP(host) == nil {
		return errors.New("invalid SIP listen address")
	}
	if ip := net.ParseIP(c.AdvertiseIP); ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return errors.New("advertise_ip must be a concrete IPv4 address")
	}
	pbx, err := net.ResolveUDPAddr("udp4", c.PBX)
	if err != nil || pbx.Port == 0 || pbx.IP.To4() == nil || pbx.IP.IsUnspecified() {
		return errors.New("invalid PBX address")
	}
	var uri sip.Uri
	if strings.ContainsAny(c.AllowedURI, "\r\n\x00") || sip.ParseUri(c.AllowedURI, &uri) != nil || uri.Scheme != "sip" || uri.User == "" || uri.Host == "" || uri.Password != "" {
		return errors.New("allowed URI must be a SIP destination without a password")
	}
	if !safeID.MatchString(c.FromUser) {
		return errors.New("invalid SIP from user")
	}
	if c.ContactUser != "" && !safeID.MatchString(c.ContactUser) {
		return errors.New("invalid SIP contact user")
	}
	u, err := url.Parse(c.GoliveURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("golive URL must be an HTTP(S) origin")
	}
	if c.GoliveAudioFormat != "" && c.GoliveAudioFormat != "g711" && c.GoliveAudioFormat != "pcm16" {
		return errors.New("golive_audio_format must be g711 or pcm16")
	}
	if c.Codec != "pcma" && c.Codec != "pcmu" {
		return errors.New("codec must be pcma or pcmu")
	}
	if c.RTPMin < 1024 || c.RTPMax > 65534 || c.RTPMin > c.RTPMax {
		return errors.New("invalid RTP port range")
	}
	if c.QueueMS < 40 || c.QueueMS > 2000 {
		return errors.New("queue_ms must be 40..2000")
	}
	if c.RingTimeout < time.Second || c.RingTimeout > 2*time.Minute || c.MaxDuration < time.Second || c.MaxDuration > time.Hour {
		return errors.New("invalid call duration limits")
	}
	if len(c.MediaNetworks) == 0 {
		return errors.New("at least one allowed media CIDR is required")
	}
	if c.RecordDir == "" {
		return errors.New("private record directory required")
	}
	return os.MkdirAll(c.RecordDir, 0700)
}

// Validate checks operator configuration without placing a call.
func (c Config) Validate() error { return c.validate() }

// LoadConfig rejects misspelled fields. Credentials are read only from the
// environment, so the example configuration can remain safe to commit.
func LoadConfig(src io.Reader) (Config, error) {
	c := Config{SIPListen: "127.0.0.1:15060", AdvertiseIP: "127.0.0.1", FromUser: "golive", GoliveURL: "http://127.0.0.1:9875", Codec: "pcma", RTPMin: 31000, RTPMax: 31031, QueueMS: 500}
	v := struct {
		*Config
		RingSeconds     int `json:"ring_timeout_seconds"`
		DurationSeconds int `json:"max_duration_seconds"`
	}{&c, 30, 600}
	d := json.NewDecoder(io.LimitReader(src, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	if v.RingSeconds < 1 || v.RingSeconds > 120 || v.DurationSeconds < 1 || v.DurationSeconds > 3600 {
		return c, errors.New("invalid duration seconds")
	}
	c.RingTimeout = time.Duration(v.RingSeconds) * time.Second
	c.MaxDuration = time.Duration(v.DurationSeconds) * time.Second
	c.SIPPassword = os.Getenv("GOLIVE_SIP_PASSWORD")
	c.GoliveToken = os.Getenv("GOLIVE_ADAPTER_TOKEN")
	return c, c.validate()
}

func (c Config) payloadType() byte {
	if c.Codec == "pcmu" {
		return 0
	}
	return 8
}
func (c Config) wireFormat() string {
	if c.Codec == "pcmu" {
		return "g711_ulaw"
	}
	return "g711_alaw"
}
func (c Config) silence() byte {
	if c.Codec == "pcmu" {
		return 0xff
	}
	return 0xd5
}
