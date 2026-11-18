package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	Listen      string
	APIUpstream string // e.g. https://api.blockchain.poker
	WebUpstream string // e.g. https://blockchain.poker
	PublicURL   string // how browsers reach this proxy, e.g. http://localhost:8080
}

type Server struct {
	cfg   Config
	api   *httputil.ReverseProxy
	web   *httputil.ReverseProxy
	rec   *Recorder
	GW    http.Handler // optional b52 gateway, mounted at /v1/
	Relay http.Handler // optional chain-relay adapter, mounted at /ws
	apiU  *url.URL
	webU  *url.URL
}

func NewServer(cfg Config, rec *Recorder) (*Server, error) {
	apiU, err := url.Parse(cfg.APIUpstream)
	if err != nil {
		return nil, err
	}
	webU, err := url.Parse(cfg.WebUpstream)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, rec: rec, apiU: apiU, webU: webU}

	// Long-polling fallbacks and socket.io.js itself.
	s.api = &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(apiU)
		pr.Out.Host = apiU.Host
		pr.Out.Header.Set("Origin", s.webOrigin())
	}}
	// Static front end; bundles are rewritten so the API origin points back at us.
	s.web = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(webU)
			pr.Out.Host = webU.Host
			pr.Out.Header.Set("Accept-Encoding", "identity") // we need to read the body
		},
		ModifyResponse: s.rewriteResponse,
	}
	return s, nil
}

func (s *Server) webOrigin() string { return s.webU.Scheme + "://" + s.webU.Host }

func (s *Server) rewriteResponse(resp *http.Response) error {
	ct := resp.Header.Get("Content-Type")
	if !(strings.Contains(ct, "html") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json")) {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	body = s.rewriteBody(body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Etag")
	return nil
}

func (s *Server) rewriteBody(b []byte) []byte {
	pub := strings.TrimRight(s.cfg.PublicURL, "/")
	wsPub := "ws" + strings.TrimPrefix(pub, "http") // http->ws, https->wss
	for _, scheme := range []string{"https://", "wss://"} {
		old := scheme + s.apiU.Host
		repl := pub
		if scheme == "wss://" {
			repl = wsPub
		}
		b = bytes.ReplaceAll(b, []byte(old), []byte(repl))
	}
	return b
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/") && s.GW != nil:
		s.GW.ServeHTTP(w, r)
	case r.URL.Path == "/ws" && s.Relay != nil:
		s.Relay.ServeHTTP(w, r)
	case r.URL.Path == "/healthz":
		fmt.Fprintln(w, "ok")
	case strings.HasPrefix(r.URL.Path, "/socket.io/"):
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			s.relayWS(w, r)
			return
		}
		s.api.ServeHTTP(w, r)
	default:
		s.web.ServeHTTP(w, r)
	}
}

func newSID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) relayWS(w http.ResponseWriter, r *http.Request) {
	sid := newSID()
	up := *s.apiU
	up.Scheme = map[string]string{"https": "wss", "http": "ws"}[s.apiU.Scheme]
	up.Path = r.URL.Path
	up.RawQuery = r.URL.RawQuery

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hdr := http.Header{}
	hdr.Set("Origin", s.webOrigin())
	if ua := r.Header.Get("User-Agent"); ua != "" {
		hdr.Set("User-Agent", ua)
	}
	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	upConn, _, err := websocket.Dial(dctx, up.String(), &websocket.DialOptions{HTTPHeader: hdr})
	dcancel()
	if err != nil {
		log.Printf("[%s] upstream dial failed: %v", sid, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upConn.CloseNow()
	upConn.SetReadLimit(16 << 20)

	client, err := websocket.Accept(w, r, nil) // same-origin check by default
	if err != nil {
		log.Printf("[%s] accept failed: %v", sid, err)
		return
	}
	defer client.CloseNow()
	client.SetReadLimit(16 << 20)
	log.Printf("[%s] ws open %s", sid, r.URL.RawQuery)

	pump := func(dir string, from, to *websocket.Conn) <-chan error {
		done := make(chan error, 1)
		go func() {
			for {
				typ, data, err := from.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				s.rec.Frame(sid, dir, typ == websocket.MessageBinary, data)
				if err := to.Write(ctx, typ, data); err != nil {
					done <- err
					return
				}
			}
		}()
		return done
	}
	var cause error
	select {
	case cause = <-pump("c2s", client, upConn):
	case cause = <-pump("s2c", upConn, client):
	}
	cancel()
	log.Printf("[%s] ws closed: %v", sid, cause)
}
