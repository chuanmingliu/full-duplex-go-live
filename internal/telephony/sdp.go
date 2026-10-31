package telephony

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func offerSDP(ip string, port int, pt byte) []byte {
	codec := "PCMA"
	if pt == 0 {
		codec = "PCMU"
	}
	return []byte(fmt.Sprintf("v=0\r\no=golive 1 1 IN IP4 %s\r\ns=golive\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP %d\r\na=rtpmap:%d %s/8000\r\na=ptime:20\r\na=sendrecv\r\n", ip, ip, port, pt, pt, codec))
}

// parseAnswer accepts only the offered static G.711 codec on unicast IPv4.
// Reject unsupported SDP rather than silently decoding the wrong wire format.
func parseAnswer(body []byte, pt byte, allowed []netip.Prefix) (*net.UDPAddr, error) {
	return parseMediaAnswer(body, pt, allowed, false)
}

func parseMediaAnswer(body []byte, pt byte, allowed []netip.Prefix, early bool) (*net.UDPAddr, error) {
	if len(body) > 16<<10 {
		return nil, errors.New("SDP too large")
	}
	var sessionIP, mediaIP string
	port, audioCount := 0, 0
	inMedia, inAudio, foundCodec := false, false, false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "m=") {
			inMedia = true
			inAudio = strings.HasPrefix(line, "m=audio ")
			if !inAudio {
				continue
			}
			audioCount++
			f := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(f) < 4 || f[2] != "RTP/AVP" {
				return nil, errors.New("unsupported media transport")
			}
			var err error
			port, err = strconv.Atoi(f[1])
			if err != nil || port < 1 || port > 65535 {
				return nil, errors.New("invalid media port")
			}
			for _, x := range f[3:] {
				if x == strconv.Itoa(int(pt)) {
					foundCodec = true
				}
			}
		}
		if strings.HasPrefix(line, "c=") {
			f := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(f) != 3 || f[0] != "IN" || f[1] != "IP4" {
				return nil, errors.New("unsupported media address")
			}
			if !inMedia {
				sessionIP = f[2]
			} else if inAudio {
				mediaIP = f[2]
			}
		}
		if (!inMedia || inAudio) && (line == "a=inactive" || line == "a=recvonly" || (line == "a=sendonly" && !early)) {
			return nil, errors.New("bidirectional audio required")
		}
		if inAudio && strings.HasPrefix(line, fmt.Sprintf("a=rtpmap:%d ", pt)) {
			want := "PCMA/8000"
			if pt == 0 {
				want = "PCMU/8000"
			}
			v := strings.TrimPrefix(line, fmt.Sprintf("a=rtpmap:%d ", pt))
			if !strings.EqualFold(v, want) && !strings.EqualFold(v, want+"/1") {
				return nil, errors.New("conflicting codec mapping")
			}
		}
	}
	if audioCount != 1 || !foundCodec {
		return nil, errors.New("answer must accept exactly one G.711 audio stream")
	}
	if mediaIP == "" {
		mediaIP = sessionIP
	}
	ip, err := netip.ParseAddr(mediaIP)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, errors.New("invalid unicast media address")
	}
	for _, prefix := range allowed {
		if prefix.Contains(ip) {
			return &net.UDPAddr{IP: net.IP(ip.AsSlice()), Port: port}, nil
		}
	}
	return nil, errors.New("media address is outside allowed CIDRs")
}
