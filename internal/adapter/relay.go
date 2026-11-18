package adapter

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/block52/blockchainpoker_proxy/internal/gateway"
	"github.com/coder/websocket"
)

// Upstream is the slice of a gateway session the relay needs (gateway.Handle satisfies it).
type Upstream interface {
	Emit(ctx context.Context, event string, payload any) (json.RawMessage, error)
	Subscribe() (<-chan gateway.Event, func())
	Done() <-chan struct{}
}

// Opener returns the upstream session for a b52 address.
type Opener func(ctx context.Context, addr string) (Upstream, error)

// Relay speaks the Block 52 chain relay websocket protocol (GET /ws?tableAddress=&playerId=,
// first frame {type:"subscribe",...}) and answers with "state" frames built from blockchain.poker.
//
// POC limits: view-only (no legal actions); state is refreshed by re-reading the table after
// game events rather than applying each event; the subscribe signature is NOT verified, so the
// endpoint only serves loopback clients.
type Relay struct {
	Open       Opener
	Origins    []string      // allowed browser origins (host patterns), e.g. "localhost:*"
	MinRefresh time.Duration // floor between table re-reads
}

// gameEvents are upstream pushes that mean "the table changed".
var gameEvents = map[string]bool{
	"handStart": true, "preFlop": true, "flop": true, "turn": true, "river": true, "fold": true, "call": true,
	"check": true, "raise": true, "allin": true, "sit": true, "stand": true, "sitIn": true, "sitOut": true,
	"topUp": true, "handEnd": true, "roundEnd": true, "readyForNextHand": true, "turnStart": true, "turnEnd": true,
	"myTableUpsert": true, "myTableTurn": true, "reveal": true, "elimination": true,
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// eventTable returns the table id an upstream push refers to, or 0 if it names none.
func eventTable(data json.RawMessage) int64 {
	var m struct {
		Table   json.Number `json:"table"`
		TableID json.Number `json:"tableId"`
	}
	if json.Unmarshal(data, &m) != nil {
		return 0
	}
	for _, n := range []json.Number{m.Table, m.TableID} {
		if v, err := n.Int64(); err == nil {
			return v
		}
	}
	return 0
}

// Snapshot reads the table and the b52 user's seat/cards and builds the frame.
func Snapshot(ctx context.Context, up Upstream, tableID int64, playerAddr string) (Frame, error) {
	raw, err := up.Emit(ctx, "join", map[string]any{"tableId": tableID, "password": nil, "fromTable": nil, "currency": nil})
	if err != nil {
		return Frame{}, err
	}
	tb, err := DecodeJoin(raw)
	if err != nil {
		return Frame{}, err
	}
	hero := Hero{Address: playerAddr, SeatIndex: -1}
	if mine, err := up.Emit(ctx, "getMyTables", map[string]any{}); err == nil {
		var r struct {
			Tables []struct {
				ID            int64  `json:"id"`
				YourSeatIndex int    `json:"yourSeatIndex"`
				HoleCards     []Card `json:"holeCards"`
			} `json:"tables"`
		}
		if json.Unmarshal(mine, &r) == nil {
			for _, m := range r.Tables {
				if m.ID == tableID {
					hero.SeatIndex, hero.Hole = m.YourSeatIndex, m.HoleCards
				}
			}
		}
	}
	return BuildFrame(tb, hero), nil
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/ws" {
		http.NotFound(w, req)
		return
	}
	if !isLoopback(req.RemoteAddr) {
		http.Error(w, "relay adapter serves loopback clients only (POC: subscribe signature not verified)", http.StatusForbidden)
		return
	}
	tableID, err := strconv.ParseInt(req.URL.Query().Get("tableAddress"), 10, 64)
	player := req.URL.Query().Get("playerId")
	if err != nil || player == "" {
		http.Error(w, "tableAddress (numeric) and playerId required", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, req, &websocket.AcceptOptions{OriginPatterns: r.Origins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	// The chain protocol opens with a subscribe frame; wait for it.
	rctx, rc := context.WithTimeout(ctx, 15*time.Second)
	_, first, err := c.Read(rctx)
	rc()
	var sub struct {
		Type string `json:"type"`
	}
	if err != nil || json.Unmarshal(first, &sub) != nil || sub.Type != "subscribe" {
		c.Close(websocket.StatusPolicyViolation, "expected subscribe")
		return
	}

	up, err := r.Open(ctx, player)
	if err != nil {
		log.Printf("relay: open upstream for table %d: %v", tableID, err)
		send(ctx, c, map[string]string{"type": "error", "code": "UPSTREAM", "message": "blockchain.poker session unavailable"})
		c.Close(websocket.StatusInternalError, "upstream")
		return
	}
	events, stop := up.Subscribe()
	defer stop()

	// reader: only to notice the client closing.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	min := r.MinRefresh
	if min == 0 {
		min = 750 * time.Millisecond
	}
	refresh := func() {
		f, err := Snapshot(ctx, up, tableID, player)
		if err != nil {
			log.Printf("relay: snapshot table %d: %v", tableID, err)
			send(ctx, c, map[string]string{"type": "error", "code": "UPSTREAM", "message": err.Error(), "gameId": strconv.FormatInt(tableID, 10)})
			return
		}
		send(ctx, c, f)
	}
	refresh()
	last := time.Now()
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-up.Done():
			c.Close(websocket.StatusGoingAway, "upstream closed")
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !gameEvents[ev.Name] {
				continue
			}
			if t := eventTable(ev.Data); t != 0 && t != tableID {
				continue
			}
			if timer == nil { // coalesce a burst into one trailing refresh
				wait := min - time.Since(last)
				if wait < 50*time.Millisecond {
					wait = 50 * time.Millisecond
				}
				timer = time.After(wait)
			}
		case <-timer:
			timer = nil
			refresh()
			last = time.Now()
		}
	}
}

func send(ctx context.Context, c *websocket.Conn, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = c.Write(wctx, websocket.MessageText, b)
}

// OpenerFor adapts a gateway to the relay's Opener.
func OpenerFor(g *gateway.Gateway) Opener {
	return func(ctx context.Context, addr string) (Upstream, error) {
		h, err := g.Open(ctx, addr)
		if err != nil {
			return nil, err
		}
		return h, nil
	}
}
