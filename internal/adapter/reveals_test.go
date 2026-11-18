package adapter

import (
	"encoding/json"
	"os"
	"testing"
)

func holeOf(f Frame, seat int) []string {
	for _, p := range f.Data.GameState.Players {
		if p.Seat == seat {
			return p.HoleCards
		}
	}
	return nil
}

func TestHandEndShowsRevealedHoleCardsAndBoard(t *testing.T) {
	raw, err := os.ReadFile("testdata/hand_end.json")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := DecodeTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := BuildFrame(tb, Hero{SeatIndex: -1})
	gs := f.Data.GameState
	if gs.Round != "showdown" {
		t.Fatalf("a finished hand with revealed cards is a showdown, got %q", gs.Round)
	}
	if len(gs.CommunityCards) != 5 {
		t.Fatalf("board %v", gs.CommunityCards)
	}
	shown := 0
	for _, s := range tb.Seats {
		if !s.RevealHand {
			continue
		}
		h := holeOf(f, s.Index+1)
		if len(h) != 2 || h[0] == "X" || h[1] == "X" {
			t.Fatalf("seat %d revealed but shows %v", s.Index+1, h)
		}
		shown++
	}
	if shown != 2 {
		t.Fatalf("expected 2 revealed hands in the fixture, saw %d", shown)
	}
}

func TestBoardFallsBackToRoundsWhenCardsCleared(t *testing.T) {
	raw, _ := os.ReadFile("testdata/hand_end.json")
	tb, _ := DecodeTable(raw)
	tb.Cards = nil // handEnd of a hand won preflop-style payloads clear the top-level board
	if got := len(BuildFrame(tb, Hero{SeatIndex: -1}).Data.GameState.CommunityCards); got != 5 {
		t.Fatalf("board from rounds: %d cards", got)
	}
}

func TestAllInRevealPersistsForTheSameHandOnly(t *testing.T) {
	// fixture(t) decodes a fresh table each call (Seats must not be shared between cases).
	var rev Reveals
	rev.LearnRoundEnd(json.RawMessage(`{"table":53803,"handId":3205243,"seats":[{"seatIndex":1,"hand":[` +
		`{"holeCard":true,"rank":"A","suit":"SPADES"},{"holeCard":true,"rank":"K","suit":"HEARTS"}]}]}`))

	same := fixture(t) // hand 3205243, seat index 1 is a live, masked opponent
	rev.Apply(&same)
	if h := holeOf(BuildFrame(same, Hero{SeatIndex: -1}), 2); len(h) != 2 || h[0] != "AS" || h[1] != "KH" {
		t.Fatalf("all-in reveal not applied: %v", h)
	}

	next := fixture(t)
	next.HandID++ // a later re-read of the NEXT hand must not inherit the reveal
	rev.Apply(&next)
	if h := holeOf(BuildFrame(next, Hero{SeatIndex: -1}), 2); len(h) != 2 || h[0] != "X" {
		t.Fatalf("reveal leaked into the next hand: %v", h)
	}

	// learning a table of a new hand resets the memory
	rev.LearnTable(next)
	again := fixture(t)
	rev.Apply(&again)
	if h := holeOf(BuildFrame(again, Hero{SeatIndex: -1}), 2); h[0] != "X" {
		t.Fatalf("stale reveal survived a hand change: %v", h)
	}
}
