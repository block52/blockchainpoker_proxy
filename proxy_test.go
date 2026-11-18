package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// fake upstream: serves a JS bundle with the API origin baked in, and a socket.io-ish ws.
func fakeUpstream(t *testing.T, apiHost string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/main.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`io("https://` + apiHost + `",{transports:["websocket"]}); ws("wss://` + apiHost + `")`))
	})
	mux.HandleFunc("/socket.io/", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		c.Write(ctx, websocket.MessageText, []byte(`0{"sid":"abc","pingInterval":25000}`))
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			s := string(data)
			if strings.HasPrefix(s, `421["gettables"`) {
				c.Write(ctx, websocket.MessageText, []byte(`431[{"tables":[]}]`))
			}
		}
	})
	return httptest.NewServer(mux)
}

func TestProxyEndToEnd(t *testing.T) {
	var up *httptest.Server
	up = fakeUpstream(t, "api.example.test")
	defer up.Close()

	buf := &syncBuf{}
	srv, err := NewServer(Config{APIUpstream: up.URL, WebUpstream: up.URL, PublicURL: "http://proxy.local"}, NewRecorder(buf, false))
	if err != nil {
		t.Fatal(err)
	}
	// fakeUpstream bakes in the API *host*; the proxy rewrites scheme://apiHost, so point apiU.Host at it.
	srv.apiU.Host = "api.example.test"
	p := httptest.NewServer(srv)
	defer p.Close()

	// 1. bundle rewrite
	resp, err := http.Get(p.URL + "/main.js")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	body.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(body.String(), `io("http://proxy.local"`) || !strings.Contains(body.String(), `ws("ws://proxy.local"`) {
		t.Fatalf("bundle not rewritten: %s", body.String())
	}
	if resp.ContentLength != int64(body.Len()) {
		t.Fatalf("content-length %d != %d", resp.ContentLength, body.Len())
	}

	// 2. websocket relay + recording. Point the API upstream back at the fake for dialing.
	srv.apiU.Host = strings.TrimPrefix(up.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.URL, "http")+"/socket.io/?EIO=4&transport=websocket", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, d, err := c.Read(ctx)
	if err != nil || !strings.HasPrefix(string(d), "0{") {
		t.Fatalf("open packet: %q %v", d, err)
	}
	c.Write(ctx, websocket.MessageText, []byte(`40`))
	c.Write(ctx, websocket.MessageText, []byte(`421["gettables",{"x":1}]`))
	_, d, err = c.Read(ctx)
	if err != nil || string(d) != `431[{"tables":[]}]` {
		t.Fatalf("ack: %q %v", d, err)
	}
	c.Write(ctx, websocket.MessageText, []byte(`422["authenticate",{"username":"bob","authToken":"SECRET123","password":"hunter2"}]`))
	time.Sleep(100 * time.Millisecond)

	log := buf.String()
	var ack Entry
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad jsonl %q: %v", line, err)
		}
		if e.Dir == "s2c" && e.Kind == "text" {
			ack = e
		}
	}
	if ack.Event != "gettables#ack" {
		t.Fatalf("ack not labelled, got %+v\n%s", ack, log)
	}
	if strings.Contains(log, "SECRET123") || strings.Contains(log, "hunter2") {
		t.Fatalf("credentials leaked into capture:\n%s", log)
	}
	if !strings.Contains(log, `"event":"authenticate"`) {
		t.Fatalf("authenticate event not recorded:\n%s", log)
	}
}

func TestParseFrame(t *testing.T) {
	cases := []struct {
		in         string
		sio        byte
		ack, event string
	}{
		{`42["fold",{}]`, '2', "", "fold"},
		{`4212["join",{"a":1}]`, '2', "12", "join"},
		{`42/admin,7["x"]`, '2', "7", "x"},
		{`437[{"ok":true}]`, '3', "7", ""},
	}
	for _, c := range cases {
		_, sio, ack, ev := parseFrame(c.in)
		if sio != c.sio || ack != c.ack || ev != c.event {
			t.Errorf("%s -> sio=%c ack=%q ev=%q", c.in, sio, ack, ev)
		}
	}
}
