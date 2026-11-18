package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeBP is a tiny Engine.IO v4 / Socket.IO v5 server standing in for api.blockchain.poker.
type fakeBP struct {
	*httptest.Server
	mu     sync.Mutex
	events []string // event names received, in order
	auths  []map[string]any
}

func newFakeBP(t *testing.T) *fakeBP {
	f := &fakeBP{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		c.Write(ctx, websocket.MessageText, []byte(`0{"sid":"s","pingInterval":25000,"pingTimeout":20000}`))
		for {
			_, d, err := c.Read(ctx)
			if err != nil {
				return
			}
			s := string(d)
			if s == "40" {
				c.Write(ctx, websocket.MessageText, []byte(`40{"sid":"n"}`))
				continue
			}
			if !strings.HasPrefix(s, "42") {
				continue
			}
			rest := s[2:]
			i := 0
			for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
				i++
			}
			id := rest[:i]
			var arr []json.RawMessage
			json.Unmarshal([]byte(rest[i:]), &arr)
			var ev string
			json.Unmarshal(arr[0], &ev)
			f.mu.Lock()
			f.events = append(f.events, ev)
			f.mu.Unlock()
			switch ev {
			case "authenticate":
				var p map[string]any
				json.Unmarshal(arr[1], &p)
				f.mu.Lock()
				f.auths = append(f.auths, p)
				f.mu.Unlock()
				c.Write(ctx, websocket.MessageText, []byte(`43`+id+`[{"authToken":"tok-1","account":{"name":"Troubadours","secret":"sec-1"}}]`))
			case "join":
				c.Write(ctx, websocket.MessageText, []byte(`43`+id+`[{"table":{"id":53483}}]`))
				c.Write(ctx, websocket.MessageText, []byte(`42["handStart",{"handId":7}]`))
			default:
				c.Write(ctx, websocket.MessageText, []byte(`43`+id+`[{"ok":true}]`))
			}
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBP) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func wsURL(u string) string { return "ws" + strings.TrimPrefix(u, "http") }

func dialUI(t *testing.T, gw *httptest.Server, addr, secret string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL(gw.URL)+"/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + secret}, "X-B52-Address": {addr}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func call(t *testing.T, c *websocket.Conn, id, event, data string) (ack json.RawMessage, pushes []out) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := `{"id":"` + id + `","event":"` + event + `","data":` + data + `}`
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	for {
		_, d, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for ack %s: %v (pushes %v)", id, err, pushes)
		}
		var o out
		json.Unmarshal(d, &o)
		if o.ID == id {
			return o.Ack, pushes
		}
		pushes = append(pushes, o)
	}
}

func TestGatewayProvisionRelayAndAllowlist(t *testing.T) {
	bp := newFakeBP(t)
	store := &MemStore{}
	g := New(Config{UpstreamWS: wsURL(bp.URL) + "/socket.io/?EIO=4&transport=websocket", CaptchaToken: "partner-bypass"},
		DevAuth{Secret: "dev"}, store)
	gw := httptest.NewServer(g)
	defer gw.Close()

	// unauthenticated callers never reach upstream
	resp, _ := http.Get(gw.URL + "/v1/ws")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}

	ui := dialUI(t, gw, "b521abc", "dev")

	// first use provisioned a guest account and stored its creds against the b52 address
	creds, ok := store.Get("b521abc")
	if !ok || creds.Name != "Troubadours" || creds.Secret != "sec-1" || creds.AuthToken != "tok-1" {
		t.Fatalf("creds not stored: %+v %v", creds, ok)
	}
	if a := bp.auths[0]; a["isNewUserAttempt"] != true || a["captchaToken"] != "partner-bypass" {
		t.Fatalf("provision payload: %v", a)
	}

	// allow-listed action is relayed and its upstream push comes back
	ack, pushes := call(t, ui, "1", "join", `{"tableId":53483}`)
	if !strings.Contains(string(ack), `"id":53483`) {
		t.Fatalf("join ack: %s", ack)
	}
	_ = pushes // handStart may arrive before or after the ack
	// read the push if it wasn't already seen
	if len(pushes) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, d, err := ui.Read(ctx)
		if err != nil || !strings.Contains(string(d), `"event":"handStart"`) {
			t.Fatalf("expected handStart push, got %s %v", d, err)
		}
	}

	// money/credential events are refused at the gateway and never emitted upstream
	for i, ev := range []string{"withdraw", "updatePassword", "startWithdrawal", "setEmail"} {
		ack, _ := call(t, ui, "f"+strconv.Itoa(i), ev, `{}`)
		if !strings.Contains(string(ack), "FORBIDDEN_EVENT") {
			t.Fatalf("%s not refused: %s", ev, ack)
		}
	}
	for _, ev := range bp.seen() {
		if ev == "withdraw" || ev == "updatePassword" || ev == "startWithdrawal" || ev == "setEmail" {
			t.Fatalf("forbidden event reached upstream: %v", bp.seen())
		}
	}

	// a second UI connection for the same address shares the one upstream session
	_ = dialUI(t, gw, "b521abc", "dev")
	n := 0
	for _, ev := range bp.seen() {
		if ev == "authenticate" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 upstream authenticate, got %d", n)
	}
}

func TestGatewayRequiresCaptchaToProvision(t *testing.T) {
	bp := newFakeBP(t)
	g := New(Config{UpstreamWS: wsURL(bp.URL) + "/socket.io/?EIO=4&transport=websocket"}, DevAuth{Secret: "dev"}, &MemStore{})
	gw := httptest.NewServer(g)
	defer gw.Close()
	req, _ := http.NewRequest("GET", gw.URL+"/v1/ws", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("X-B52-Address", "b52new")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	if len(bp.seen()) != 0 && contains(bp.seen(), "authenticate") {
		t.Fatalf("must not call upstream authenticate without a bypass token")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestFileStoreEncryptedRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "creds.enc")
	s, err := NewFileStore(p, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b52a", Creds{Name: "n", AuthToken: "AUTHTOK", Secret: "SECRETVAL"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "SECRETVAL") || strings.Contains(string(raw), "AUTHTOK") {
		t.Fatal("plaintext in store file")
	}
	s2, err := NewFileStore(p, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := s2.Get("b52a"); !ok || c.Secret != "SECRETVAL" {
		t.Fatalf("reload: %+v", c)
	}
	if _, err := NewFileStore(p, "wrong"); err == nil {
		t.Fatal("wrong key must fail")
	}
}
