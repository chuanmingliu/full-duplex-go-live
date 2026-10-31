package telephony

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type readyConn struct {
	net.PacketConn
	ready chan struct{}
	once  sync.Once
}

func (c *readyConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.once.Do(func() { close(c.ready) })
	return c.PacketConn.ReadFrom(b)
}

type sipEndpoint struct {
	ua      *sipgo.UserAgent
	client  *sipgo.Client
	dialogs *sipgo.DialogClientCache
	conn    *readyConn
	done    chan error
}

func newSIPEndpoint(c Config) (*sipEndpoint, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("golive-telephony"), sipgo.WithUserAgentHostname(c.AdvertiseIP))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sipEndpoint, error) { ua.Close(); return nil, err }
	addr, _ := net.ResolveUDPAddr("udp4", c.SIPListen)
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fail(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(c.AdvertiseIP), sipgo.WithClientPort(port), sipgo.WithClientConnectionAddr(conn.LocalAddr().String()))
	if err != nil {
		conn.Close()
		return fail(err)
	}
	contactUser := c.ContactUser
	if contactUser == "" {
		contactUser = c.FromUser
	}
	dialogs := sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: contactUser, Host: c.AdvertiseIP, Port: port}})
	server, err := sipgo.NewServer(ua)
	if err != nil {
		conn.Close()
		return fail(err)
	}
	pbx, _ := net.ResolveUDPAddr("udp4", c.PBX)
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		source, e := net.ResolveUDPAddr("udp4", req.Source())
		if e != nil || !source.IP.Equal(pbx.IP) || source.Port != pbx.Port {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 403, "Forbidden", nil))
			return
		}
		if dialogs.ReadBye(req, tx) != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "No Dialog", nil))
		}
	})
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 405, "Outbound Only", nil))
	})
	server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	e := &sipEndpoint{ua: ua, client: client, dialogs: dialogs, conn: &readyConn{PacketConn: conn, ready: make(chan struct{})}, done: make(chan error, 1)}
	go func() { e.done <- server.ServeUDP(e.conn) }()
	select {
	case <-e.conn.ready:
		return e, nil
	case err := <-e.done:
		conn.Close()
		return fail(err)
	}
}

func (e *sipEndpoint) close() { e.conn.Close(); e.ua.Close(); <-e.done }

// Keep receiving the INVITE transaction after cancellation: a racing 200 must
// still be ACKed and BYEd. sipgo's default cancellation path discards that case.
func (e *sipEndpoint) invite(ctx context.Context, c Config, id string, port int, r *recorder, early func(*net.UDPAddr)) (*sipgo.DialogClientSession, error) {
	var uri sip.Uri
	_ = sip.ParseUri(c.AllowedURI, &uri)
	req := sip.NewRequest(sip.INVITE, uri)
	req.SetDestination(c.PBX)
	req.SetTransport("UDP")
	params := sip.NewParams()
	params.Add("tag", sip.GenerateTagN(16))
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: c.FromUser, Host: c.AdvertiseIP}, Params: params})
	callID := sip.CallIDHeader(id + "@" + c.AdvertiseIP)
	req.AppendHeader(&callID)
	content := sip.ContentTypeHeader("application/sdp")
	req.AppendHeader(&content)
	req.SetBody(offerSDP(c.AdvertiseIP, port, c.payloadType()))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	d, err := e.dialogs.WriteInvite(ctx, req)
	if err != nil {
		return nil, errors.New("SIP INVITE send failed")
	}
	answerCtx, force := context.WithCancelCause(context.Background())
	defer force(sipgo.WaitAnswerForceCancelErr)
	provisionals := make(chan *sip.Request, 16)
	result := make(chan error, 1)
	go func() {
		result <- d.WaitAnswer(answerCtx, sipgo.AnswerOptions{Username: c.SIPUsername, Password: c.SIPPassword, OnResponse: func(res *sip.Response) error {
			r.event("sip_response", map[string]any{"status": res.StatusCode})
			// Do not start a new authenticated INVITE after cancellation. Final
			// success still goes through ACK/BYE even if the caller has stopped.
			if ctx.Err() != nil && !res.IsSuccess() && !res.IsProvisional() {
				return ctx.Err()
			}
			if res.IsProvisional() {
				if early != nil && (res.StatusCode == 180 || res.StatusCode == 183) && len(res.Body()) > 0 && ctx.Err() == nil {
					if peer, err := parseMediaAnswer(res.Body(), c.payloadType(), c.MediaNetworks, true); err == nil {
						early(peer)
						r.event("early_media", map[string]any{"status": res.StatusCode})
					} else {
						r.event("early_media_rejected", map[string]any{"status": res.StatusCode})
					}
				}
				provisionals <- d.InviteRequest.Clone()
			}
			return nil
		}})
	}()
	cancelled := ctx.Done()
	var latest *sip.Request
	var deadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	sent := make(map[string]bool)
	sendCancel := func() {
		if latest == nil {
			return
		}
		key := latest.Via().Value() + latest.CSeq().Value()
		if sent[key] {
			return
		}
		sent[key] = true
		cancelReq := latest.Clone()
		cancelReq.Method = sip.CANCEL
		cancelReq.CSeq().MethodName = sip.CANCEL
		cancelReq.RemoveHeader("Content-Type")
		cancelReq.RemoveHeader("Authorization")
		cancelReq.RemoveHeader("Proxy-Authorization")
		cancelReq.SetBody(nil)
		cancelCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		res, err := e.client.Do(cancelCtx, cancelReq, func(*sipgo.Client, *sip.Request) error { return nil })
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		r.event("sip_cancel", map[string]any{"transaction_ok": err == nil && status == 200, "status": status})
	}
	for {
		select {
		case latest = <-provisionals:
			if cancelled == nil {
				sendCancel()
			}
		case <-cancelled:
			cancelled = nil
			timer = time.NewTimer(33 * time.Second)
			deadline = timer.C
			sendCancel()
		case <-deadline:
			force(sipgo.WaitAnswerForceCancelErr)
			if err := <-result; err != nil {
				d.Close()
				return nil, errors.New("SIP cancellation timed out")
			}
			// A 200 may win the race with the hard deadline. Never discard it.
			return confirmDialog(d, r)
		case err := <-result:
			if err != nil {
				d.Close()
				return nil, errors.New("SIP call was not answered")
			}
			return confirmDialog(d, r)
		}
	}
}

func confirmDialog(d *sipgo.DialogClientSession, r *recorder) (*sipgo.DialogClientSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := d.Ack(ctx)
	cancel()
	if err != nil {
		endDialog(d, r)
		return nil, errors.New("SIP ACK failed")
	}
	return d, nil
}

func endDialog(d *sipgo.DialogClientSession, r *recorder) error {
	defer d.Close()
	if d.Context().Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := d.Bye(ctx)
	r.event("sip_bye", map[string]any{"transaction_ok": err == nil})
	if err != nil {
		return errors.New("SIP hangup was not acknowledged")
	}
	return nil
}
