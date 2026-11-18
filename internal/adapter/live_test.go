//go:build live

package adapter

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// RELAY=http://localhost:8081 TABLE=53803 go test -tags live -run TestLiveRelay -v ./internal/adapter
func TestLiveRelay(t *testing.T) {
	base := os.Getenv("RELAY")
	if base == "" {
		t.Skip("RELAY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws?tableAddress="+os.Getenv("TABLE")+"&playerId=b52livetest", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(4 << 20)
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","gameId":"`+os.Getenv("TABLE")+`","playerAddress":"b52livetest"}`))
	for i := 0; i < 3; i++ {
		rctx, rc := context.WithTimeout(ctx, 12*time.Second)
		_, d, err := c.Read(rctx)
		rc()
		if err != nil {
			t.Logf("frame %d: no more frames (%v)", i, err)
			return
		}
		var f Frame
		if json.Unmarshal(d, &f) != nil || f.Event != "state" {
			t.Logf("frame %d (non-state): %s", i, d)
			continue
		}
		gs := f.Data.GameState
		t.Logf("frame %d: %s format=%s round=%s hand=%d pot=%s board=%v dealer=%d next=%d players=%d avail=%v",
			i, f.Data.Name, f.Data.Format, gs.Round, gs.HandNumber, gs.TotalPot, gs.CommunityCards, gs.Dealer, gs.NextToAct, len(gs.Players), gs.AvailableSeats)
		for _, p := range gs.Players {
			t.Logf("   seat %d %-12s stack=%-6s status=%-13s hole=%v", p.Seat, p.Address, p.Stack, p.Status, p.HoleCards)
		}
	}
}
