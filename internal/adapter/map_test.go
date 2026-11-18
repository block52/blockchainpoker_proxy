package adapter

import (
	"encoding/json"
	"os"
	"testing"
)

func fixture(t *testing.T) BPTable {
	t.Helper()
	raw, err := os.ReadFile("testdata/join_ack.json")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := DecodeJoin(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestBuildFrameFromRealTable(t *testing.T) {
	tb := fixture(t)
	f := BuildFrame(tb, Hero{Address: "b52hero", SeatIndex: 5, Hole: []Card{{Suit: "HEARTS", Rank: "2"}, {Suit: "CLUBS", Rank: "A"}}})

	if f.Event != "state" || f.GameID != "53803" {
		t.Fatalf("envelope: %+v", f)
	}
	d := f.Data
	if d.Format != "tournament" || d.Variant != "texas-holdem" || d.Name != "Mundaner Decurions" {
		t.Fatalf("meta: %+v", d)
	}
	gs := d.GameState
	if gs.Round != "turn" {
		t.Fatalf("round %q", gs.Round)
	}
	if want := []string{"3C", "KD", "9S", "KH"}; len(gs.CommunityCards) != 4 || gs.CommunityCards[0] != want[0] || gs.CommunityCards[3] != want[3] {
		t.Fatalf("board %v", gs.CommunityCards)
	}
	if gs.TotalPot != "140" || len(gs.Pots) != 1 || gs.Pots[0] != "140" {
		t.Fatalf("pot %v %v", gs.TotalPot, gs.Pots)
	}
	// positions are bp 0-based -> b52 1-based: dealer 8 -> 9, sb 0 -> 1, bb 1 -> 2, active 1 -> 2
	if gs.Dealer != 9 || gs.SmallBlindPos != 1 || gs.BigBlindPos != 2 || gs.NextToAct != 2 {
		t.Fatalf("positions dealer=%d sb=%d bb=%d next=%d", gs.Dealer, gs.SmallBlindPos, gs.BigBlindPos, gs.NextToAct)
	}
	if gs.GameOptions.NextSmallBlind != "20" || gs.GameOptions.NextBigBlind != "40" {
		t.Fatalf("tournament next blinds: %+v", gs.GameOptions)
	}
	if gs.GameOptions.SmallBlind != "20" || gs.GameOptions.BigBlind != "40" || gs.GameOptions.MaxPlayers != 9 || gs.GameOptions.Timeout != 12 {
		t.Fatalf("options %+v", gs.GameOptions)
	}

	var hero, other *Player
	for i := range gs.Players {
		p := &gs.Players[i]
		if p.Seat == 6 {
			hero = p
		}
		if p.Seat == 2 {
			other = p
		}
	}
	if hero == nil || hero.Address != "b52hero" || len(hero.HoleCards) != 2 || hero.HoleCards[0] != "2H" || hero.HoleCards[1] != "AC" {
		t.Fatalf("hero %+v", hero)
	}
	if other == nil || other.Address != "bp-1001" || len(other.HoleCards) != 2 || other.HoleCards[0] != "X" || other.Status != "active" || !other.IsBigBlind {
		t.Fatalf("opponent %+v", other)
	}
	// empty seats are not players but are listed as available (1-based)
	for _, p := range gs.Players {
		if p.Seat == 3 || p.Seat == 4 || p.Seat == 8 {
			t.Fatalf("empty seat %d rendered as a player", p.Seat)
		}
	}
	if len(gs.AvailableSeats) != 3 || gs.AvailableSeats[0] != 3 {
		t.Fatalf("available %v", gs.AvailableSeats)
	}
	// seat 1 folded and sat out (timed out) -> sitting-out wins; seat 6 plainly folded
	for _, p := range gs.Players {
		if p.Seat == 1 && p.Status != "sitting-out" {
			t.Fatalf("seat 1 status %s", p.Status)
		}
		if p.Seat == 6 && p.Status != "folded" {
			t.Fatalf("seat 6 status %s", p.Status)
		}
	}
}

func TestFrameIsValidJSONWithRequiredArrays(t *testing.T) {
	b, err := json.Marshal(BuildFrame(fixture(t), Hero{SeatIndex: -1}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	gs := m["data"].(map[string]any)["gameState"].(map[string]any)
	for _, k := range []string{"players", "communityCards", "pots", "previousActions", "winners", "results", "legalActions", "availableSeats"} {
		if _, ok := gs[k].([]any); !ok {
			t.Errorf("%s must be a JSON array, got %T", k, gs[k])
		}
	}
}

func TestRoundAndFormat(t *testing.T) {
	var tb BPTable
	if Round(tb) != "end" || Format(tb) != "cash" {
		t.Fatal("empty table should be a cash table with no hand")
	}
	tb.IsHandInProgress, tb.Round = true, "PREFLOP"
	if Round(tb) != "preflop" {
		t.Fatal("preflop")
	}
	tb.Tournament, tb.IsSitNGo = "X", true
	if Format(tb) != "sit-and-go" {
		t.Fatal("sng")
	}
	if CardString(Card{Suit: "SPADES", Rank: "t"}) != "TS" {
		t.Fatal("ten")
	}
}

func TestDecodeJoinError(t *testing.T) {
	_, err := DecodeJoin(json.RawMessage(`{"error":"INVALID_PASSWORD","message":"nope"}`))
	if ue, ok := err.(*UpstreamError); !ok || ue.Code != "INVALID_PASSWORD" {
		t.Fatalf("got %v", err)
	}
}
