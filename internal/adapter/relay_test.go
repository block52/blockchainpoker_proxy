package adapter

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/block52/blockchainpoker_proxy/internal/gateway"
	"github.com/coder/websocket"
)

type fakeUp struct {
	mu     sync.Mutex
	joins  int
	join   json.RawMessage
	events chan gateway.Event
	done   chan struct{}
}

func (f *fakeUp) Emit(_ context.Context, ev string, _ any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch ev {
	case "join":
		f.joins++
		return f.join, nil
	case "getMyTables":
		return json.RawMessage(`{"tables":[{"id":53803,"yourSeatIndex":5,"holeCards":[{"rank":"2","suit":"HEARTS"},{"rank":"A","suit":"CLUBS"}]}]}`), nil
	}
	return json.RawMessage(`{}`), nil
}
func (f *fakeUp) Subscribe() (<-chan gateway.Event, func()) { return f.events, func() {} }
func (f *fakeUp) Done() <-chan struct{}                     { return f.done }
func (f *fakeUp) joinCount() int                            { f.mu.Lock(); defer f.mu.Unlock(); return f.joins }

func TestRelaySubscribeAndRefresh(t *testing.T) {
	raw, _ := os.ReadFile("testdata/join_ack.json")
	up := &fakeUp{join: raw, events: make(chan gateway.Event, 8), done: make(chan struct{})}
	var opened string
	r := &Relay{MinRefresh: 20 * time.Millisecond, Open: func(_ context.Context, addr string) (Upstream, error) { opened = addr; return up, nil }}
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?tableAddress=53803&playerId=b52hero", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	read := func() Frame {
		_, d, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var f Frame
		if err := json.Unmarshal(d, &f); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		return f
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","gameId":"53803","playerAddress":"b52hero"}`))

	f := read() // initial snapshot
	if f.Event != "state" || f.GameID != "53803" || opened != "b52hero" {
		t.Fatalf("initial: %+v opened=%q", f, opened)
	}
	var hero *Player
	for i, p := range f.Data.GameState.Players {
		if p.Address == "b52hero" {
			hero = &f.Data.GameState.Players[i]
		}
	}
	if hero == nil || hero.Seat != 6 || hero.HoleCards[0] != "2H" {
		t.Fatalf("hero not mapped: %+v", hero)
	}

	// an event for ANOTHER table is ignored; a burst for ours collapses into one refresh
	up.events <- gateway.Event{Name: "fold", Data: json.RawMessage(`{"table":99,"index":1}`)}
	up.events <- gateway.Event{Name: "playersOnline", Data: json.RawMessage(`{"numPlayersOnline":3}`)}
	before := up.joinCount()
	for i := 0; i < 5; i++ {
		up.events <- gateway.Event{Name: "call", Data: json.RawMessage(`{"table":53803,"index":1}`)}
	}
	f = read()
	if f.Event != "state" {
		t.Fatalf("refresh: %+v", f)
	}
	time.Sleep(150 * time.Millisecond)
	if got := up.joinCount() - before; got != 1 {
		t.Fatalf("burst should coalesce to 1 re-read, got %d", got)
	}
}

func TestRelayRejectsNonSubscribeAndBadQuery(t *testing.T) {
	r := &Relay{Open: func(context.Context, string) (Upstream, error) { t.Fatal("must not open upstream"); return nil, nil }}
	srv := httptest.NewServer(r)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?tableAddress=abc&playerId=x", nil); err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("bad table must be 400, err=%v", err)
	}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?tableAddress=1&playerId=x", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`))
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("expected close after non-subscribe first frame")
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:5000": true, "[::1]:5000": true, "10.1.2.3:5000": false, "bogus": false} {
		if isLoopback(addr) != want {
			t.Errorf("%s -> %v", addr, !want)
		}
	}
}

func TestSnapshotRefusesAForeignTable(t *testing.T) {
	raw, _ := os.ReadFile("testdata/join_ack.json") // table 53803
	up := &fakeUp{join: raw, events: make(chan gateway.Event), done: make(chan struct{})}
	if _, err := Snapshot(context.Background(), up, 53803, "b52x"); err != nil {
		t.Fatalf("matching table must work: %v", err)
	}
	_, err := Snapshot(context.Background(), up, 99999, "b52x") // bp would have answered with another table
	if ue, ok := err.(*UpstreamError); !ok || ue.Code != "GAME_NOT_FOUND" {
		t.Fatalf("want GAME_NOT_FOUND, got %v", err)
	}
}
