// Package gateway lets a block52 UI play on blockchain.poker as its users: the gateway holds
// each b52 address's web2 session upstream and relays allow-listed actions and table pushes.
//
//	b52 UI --(JSON over WS /v1/ws)--> gateway --(socket.io)--> api.blockchain.poker
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/block52/blockchainpoker_proxy/internal/sio"
	"github.com/coder/websocket"
)

type Config struct {
	UpstreamWS     string // wss://api.blockchain.poker/socket.io/?EIO=4&transport=websocket
	UpstreamOrigin string // https://blockchain.poker
	CaptchaToken   string // partner-issued Turnstile bypass for guest provisioning (empty = cannot provision)
	// TestUsername/TestPassword: POC test mode. Every b52 address without stored creds logs in as
	// this one existing blockchain.poker account (so only one player can be seated at a time).
	TestUsername   string
	TestPassword   string
	AllowedOrigins []string
	Allow          map[string]bool
}

// Authenticator proves which b52 address is calling. The real one verifies a wallet signature;
// DevAuth is a shared-secret stand-in for the POC.
type Authenticator interface {
	Authenticate(r *http.Request) (addr string, err error)
}

type DevAuth struct{ Secret string }

func (d DevAuth) Authenticate(r *http.Request) (string, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if d.Secret == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(d.Secret)) != 1 {
		return "", errors.New("unauthorized")
	}
	addr := r.Header.Get("X-B52-Address")
	if addr == "" {
		return "", errors.New("missing X-B52-Address")
	}
	return addr, nil
}

type Gateway struct {
	cfg   Config
	auth  Authenticator
	store CredStore

	mu       sync.Mutex
	sessions map[string]*session
}

func New(cfg Config, a Authenticator, s CredStore) *Gateway {
	if cfg.Allow == nil {
		cfg.Allow = DefaultAllow
	}
	return &Gateway{cfg: cfg, auth: a, store: s, sessions: map[string]*session{}}
}

type session struct {
	addr string
	c    *sio.Client
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct{ out chan []byte }

func (s *session) broadcast(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.out <- b:
		default: // slow consumer: drop rather than block the upstream read loop
		}
	}
}

// out is what the UI receives.
type out struct {
	ID    string          `json:"id,omitempty"`
	Ack   json.RawMessage `json:"ack,omitempty"`
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type in struct {
	ID    string          `json:"id"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/ws" {
		http.NotFound(w, r)
		return
	}
	addr, err := g.auth.Authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	sess, err := g.session(ctx, addr)
	if err != nil {
		log.Printf("gateway: session for %s: %v", short(addr), err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	sub := &subscriber{out: make(chan []byte, 256)}
	sess.mu.Lock()
	sess.subs[sub] = struct{}{}
	sess.mu.Unlock()
	defer func() { sess.mu.Lock(); delete(sess.subs, sub); sess.mu.Unlock() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // writer
		for {
			select {
			case b := <-sub.out:
				if c.Write(ctx, websocket.MessageText, b) != nil {
					cancel()
					return
				}
			case <-sess.c.Done():
				c.Close(websocket.StatusGoingAway, "upstream closed")
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m in
		if json.Unmarshal(data, &m) != nil || m.Event == "" {
			continue
		}
		go g.handle(ctx, sess, sub, m)
	}
}

func (g *Gateway) handle(ctx context.Context, sess *session, sub *subscriber, m in) {
	reply := func(ack any) {
		b, _ := json.Marshal(ack)
		o, _ := json.Marshal(out{ID: m.ID, Ack: b})
		select {
		case sub.out <- o:
		default:
		}
	}
	if !g.cfg.Allow[m.Event] {
		reply(map[string]string{"error": "FORBIDDEN_EVENT", "message": m.Event + " is not available through the gateway"})
		return
	}
	var payload any = struct{}{}
	if len(m.Data) > 0 {
		payload = m.Data
	}
	ack, err := sess.c.Emit(ctx, m.Event, payload)
	if err != nil {
		reply(map[string]string{"error": "UPSTREAM", "message": err.Error()})
		return
	}
	o, _ := json.Marshal(out{ID: m.ID, Ack: ack})
	select {
	case sub.out <- o:
	default:
	}
}

func (g *Gateway) session(ctx context.Context, addr string) (*session, error) {
	g.mu.Lock()
	if s, ok := g.sessions[addr]; ok {
		select {
		case <-s.c.Done():
			delete(g.sessions, addr)
		default:
			g.mu.Unlock()
			return s, nil
		}
	}
	g.mu.Unlock()

	s := &session{addr: addr, subs: map[*subscriber]struct{}{}}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := sio.Dial(dctx, g.cfg.UpstreamWS, g.cfg.UpstreamOrigin, func(ev string, data json.RawMessage) {
		b, _ := json.Marshal(out{Event: ev, Data: data})
		s.broadcast(b)
	})
	if err != nil {
		return nil, err
	}
	s.c = c
	if err := g.login(dctx, s); err != nil {
		c.Close()
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if prev, ok := g.sessions[addr]; ok { // lost a race: keep the winner
		select {
		case <-prev.c.Done():
		default:
			c.Close()
			return prev, nil
		}
	}
	g.sessions[addr] = s
	return s, nil
}

type authAck struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	AuthToken string `json:"authToken"`
	Account   struct {
		Name   string `json:"name"`
		Secret string `json:"secret"`
	} `json:"account"`
}

// login re-authenticates with stored creds, or provisions a guest account on first use.
// NOTE: payload fields mirror the shipped bundle; the exact re-auth contract (username vs
// secret vs authToken precedence) is unverified against live: see issue #2.
func (g *Gateway) login(ctx context.Context, s *session) error {
	creds, have := g.store.Get(s.addr)
	p := map[string]any{
		"tableId": nil, "username": "", "currency": "", "authToken": "", "secret": "", "password": "",
		"mfaToken": "", "trustedDeviceToken": "", "affiliatorKey": "", "marketingCampaignAdSlug": "",
		"isSpectator": false, "isNewUserAttempt": !have, "captchaToken": "", "theme": "felt",
	}
	if have {
		p["username"], p["authToken"], p["secret"] = creds.Name, creds.AuthToken, creds.Secret
	} else if g.cfg.TestUsername != "" {
		p["username"], p["password"], p["isNewUserAttempt"] = g.cfg.TestUsername, g.cfg.TestPassword, false
	} else {
		if g.cfg.CaptchaToken == "" {
			return errors.New("no stored account and no captcha bypass configured: cannot provision")
		}
		p["captchaToken"] = g.cfg.CaptchaToken
	}
	raw, err := s.c.Emit(ctx, "authenticate", p)
	if err != nil {
		return err
	}
	var a authAck
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("authenticate: bad ack: %w", err)
	}
	if a.Error != "" {
		return fmt.Errorf("authenticate: %s: %s", a.Error, a.Message)
	}
	nc := Creds{Name: a.Account.Name, AuthToken: a.AuthToken, Secret: a.Account.Secret}
	if have { // keep what the server doesn't echo back
		if nc.Secret == "" {
			nc.Secret = creds.Secret
		}
		if nc.AuthToken == "" {
			nc.AuthToken = creds.AuthToken
		}
	}
	return g.store.Put(s.addr, nc)
}

func short(a string) string {
	if len(a) > 10 {
		return a[:10] + "…"
	}
	return a
}

// Event is one upstream push, decoded.
type Event struct {
	Name string
	Data json.RawMessage
}

// Handle is a live upstream session for in-process consumers (the relay adapter).
type Handle struct{ s *session }

// Open returns the (shared) upstream session for a b52 address, logging in if needed.
func (g *Gateway) Open(ctx context.Context, addr string) (*Handle, error) {
	s, err := g.session(ctx, addr)
	if err != nil {
		return nil, err
	}
	return &Handle{s: s}, nil
}

func (h *Handle) Emit(ctx context.Context, event string, payload any) (json.RawMessage, error) {
	return h.s.c.Emit(ctx, event, payload)
}

// Done closes when the upstream connection drops.
func (h *Handle) Done() <-chan struct{} { return h.s.c.Done() }

// Subscribe streams upstream pushes until cancel is called. Slow consumers drop frames.
func (h *Handle) Subscribe() (<-chan Event, func()) {
	sub := &subscriber{out: make(chan []byte, 256)}
	h.s.mu.Lock()
	h.s.subs[sub] = struct{}{}
	h.s.mu.Unlock()
	ch := make(chan Event, 256)
	stop := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			select {
			case b := <-sub.out:
				var o out
				if json.Unmarshal(b, &o) == nil && o.Event != "" {
					select {
					case ch <- Event{Name: o.Event, Data: o.Data}:
					default:
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return ch, func() {
		h.s.mu.Lock()
		delete(h.s.subs, sub)
		h.s.mu.Unlock()
		close(stop)
	}
}
