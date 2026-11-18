// Package adapter translates blockchain.poker table state into the Block 52 chain relay's
// wire format (TexasHoldemStateDTO inside a "state" frame), so the unmodified b52 UI can
// render and (later) act on a blockchain.poker table through the proxy.
package adapter

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ---- blockchain.poker shapes (subset actually used) ----

type Card struct {
	Suit string `json:"suit"`
	Rank string `json:"rank"`
	// Set on seat cards: a revealed hand lists the player's two hole cards (HoleCard) among
	// the best-five cards. Masked opponents have [null,null].
	HoleCard bool `json:"holeCard"`
}

type BPSeat struct {
	Index            int     `json:"index"`
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Stack            float64 `json:"stack"`
	Bet              float64 `json:"bet"`
	PotContributions float64 `json:"potContributions"`
	IsFolded         bool    `json:"isFolded"`
	SittingOut       bool    `json:"sittingOut"`
	WasInHand        bool    `json:"wasInHand"`
	WaitingForBlind  bool    `json:"waitingForBlind"`
	Mucked           bool    `json:"mucked"`
	RevealHand       bool    `json:"revealHand"`
	Cards            []Card  `json:"cards"`
}

type BPTable struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	NumSeats         int     `json:"numSeats"`
	NumHoleCards     int     `json:"numHoleCards"`
	BuyIn            float64 `json:"buyIn"`
	MaxBuyIn         float64 `json:"maxBuyIn"`
	TurnTimeout      int     `json:"turnTimeout"`
	NextSmallBlind   float64 `json:"nextSmallBlindAmount"`
	NextBigBlind     float64 `json:"nextBigBlindAmount"`
	Round            string  `json:"round"`
	Cards            []Card  `json:"cards"`
	Pot              float64 `json:"pot"`
	HandID           int64   `json:"handId"`
	IsHandInProgress bool    `json:"isHandInProgress"`
	Tournament       string  `json:"tournament"`
	IsSitNGo         bool    `json:"isSitNGo"`
	Blinds           struct {
		Big   float64 `json:"big"`
		Small float64 `json:"small"`
	} `json:"blinds"`
	Positions struct {
		Dealer     *int `json:"dealer"`
		SmallBlind *int `json:"smallBlind"`
		BigBlind   *int `json:"bigBlind"`
		Active     *int `json:"active"`
	} `json:"positions"`
	Seats  []BPSeat `json:"seats"`
	Rounds []struct {
		Community []Card `json:"community"`
	} `json:"rounds"`
}

// Hero is the b52 user's own seat at the table (from getMyTables), whose address the UI knows.
type Hero struct {
	Address   string
	SeatIndex int // blockchain.poker 0-based; -1 = not seated
	Hole      []Card
}

// ---- b52 wire shapes (chain relay frame) ----

type Frame struct {
	Event  string    `json:"event"`
	GameID string    `json:"gameId"`
	Data   FrameData `json:"data"`
}

type FrameData struct {
	GameState State  `json:"gameState"`
	Format    string `json:"format"`
	Variant   string `json:"variant"`
	Name      string `json:"name,omitempty"`
}

type GameOptions struct {
	MinBuyIn   string `json:"minBuyIn"`
	MaxBuyIn   string `json:"maxBuyIn"`
	MinPlayers int    `json:"minPlayers"`
	MaxPlayers int    `json:"maxPlayers"`
	SmallBlind string `json:"smallBlind"`
	BigBlind   string `json:"bigBlind"`
	Timeout    int    `json:"timeout"`
	// Tournament/SNG tables must carry the next level (the UI refuses to render without it).
	NextSmallBlind string `json:"nextSmallBlind,omitempty"`
	NextBigBlind   string `json:"nextBigBlind,omitempty"`
}

type Player struct {
	Address      string   `json:"address"`
	Seat         int      `json:"seat"`
	Stack        string   `json:"stack"`
	IsSmallBlind bool     `json:"isSmallBlind"`
	IsBigBlind   bool     `json:"isBigBlind"`
	IsDealer     bool     `json:"isDealer"`
	HoleCards    []string `json:"holeCards"`
	Status       string   `json:"status"`
	LegalActions []any    `json:"legalActions"`
	SumOfBets    string   `json:"sumOfBets"`
	Timeout      int      `json:"timeout"`
	Signature    string   `json:"signature"`
}

type State struct {
	GameOptions     GameOptions `json:"gameOptions"`
	SmallBlindPos   int         `json:"smallBlindPosition"`
	BigBlindPos     int         `json:"bigBlindPosition"`
	Dealer          int         `json:"dealer"`
	Players         []Player    `json:"players"`
	CommunityCards  []string    `json:"communityCards"`
	Deck            string      `json:"deck"`
	Pots            []string    `json:"pots"`
	TotalPot        string      `json:"totalPot"`
	NextToAct       int         `json:"nextToAct"`
	PreviousActions []any       `json:"previousActions"`
	ActionCount     int         `json:"actionCount"`
	HandNumber      int64       `json:"handNumber"`
	Round           string      `json:"round"`
	Winners         []any       `json:"winners"`
	Results         []any       `json:"results"`
	LegalActions    []any       `json:"legalActions"`
	AvailableSeats  []int       `json:"availableSeats"`
	Signature       string      `json:"signature"`
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// CardString converts {suit:"HEARTS",rank:"T"} to the b52 mnemonic "TH".
func CardString(c Card) string {
	if c.Rank == "" || c.Suit == "" {
		return ""
	}
	return strings.ToUpper(c.Rank) + strings.ToUpper(c.Suit[:1])
}

func cardStrings(cs []Card) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if s := CardString(c); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Format: tournament name present => tournament (or sit-and-go); otherwise cash.
func Format(t BPTable) string {
	switch {
	case t.Tournament != "" && t.IsSitNGo:
		return "sit-and-go"
	case t.Tournament != "":
		return "tournament"
	default:
		return "cash"
	}
}

// Round maps blockchain.poker rounds. No hand in progress (or an unknown value) is "end".
func Round(t BPTable) string {
	if !t.IsHandInProgress {
		for _, s := range t.Seats {
			if len(holeOnly(s.Cards)) > 0 {
				return "showdown"
			}
		}
		return "end"
	}
	switch strings.ToUpper(t.Round) {
	case "PREFLOP":
		return "preflop"
	case "FLOP":
		return "flop"
	case "TURN":
		return "turn"
	case "RIVER":
		return "river"
	case "SHOWDOWN":
		return "showdown"
	default:
		return "end"
	}
}

func isOccupied(s BPSeat) bool { return s.Name != "" || s.ID != 0 }

func status(s BPSeat, inHand bool) string {
	switch {
	case s.SittingOut: // a timed-out player is folded AND sitting out: show the more informative state
		return "sitting-out"
	case s.IsFolded:
		return "folded"
	case s.WaitingForBlind:
		return "waiting-for-big-blind"
	case inHand && s.Stack == 0 && (s.Bet > 0 || s.PotContributions > 0):
		return "all-in"
	default:
		return "active"
	}
}

func seatOf(p *int) int { // bp 0-based -> b52 1-based; absent -> -1
	if p == nil {
		return -1
	}
	return *p + 1
}

// BuildFrame maps a blockchain.poker table to the chain relay "state" frame.
// legalActions stay empty: this slice is view-only.
func BuildFrame(t BPTable, hero Hero) Frame {
	opts := GameOptions{MinBuyIn: num(t.BuyIn), MaxBuyIn: num(t.MaxBuyIn), MinPlayers: 2, MaxPlayers: t.NumSeats,
		SmallBlind: num(t.Blinds.Small), BigBlind: num(t.Blinds.Big), Timeout: t.TurnTimeout}
	if f := Format(t); f != "cash" {
		opts.NextSmallBlind, opts.NextBigBlind = num(t.NextSmallBlind), num(t.NextBigBlind)
	}
	st := State{
		GameOptions:   opts,
		SmallBlindPos: seatOf(t.Positions.SmallBlind), BigBlindPos: seatOf(t.Positions.BigBlind), Dealer: seatOf(t.Positions.Dealer),
		Players: []Player{}, CommunityCards: cardStrings(board(t)), Pots: []string{num(t.Pot)}, TotalPot: num(t.Pot),
		NextToAct: seatOf(t.Positions.Active), PreviousActions: []any{}, HandNumber: t.HandID, Round: Round(t),
		Winners: []any{}, Results: []any{}, LegalActions: []any{}, AvailableSeats: []int{},
	}
	taken := map[int]bool{}
	for _, s := range t.Seats {
		if !isOccupied(s) {
			continue
		}
		taken[s.Index] = true
		addr := "bp-" + strconv.FormatInt(s.ID, 10)
		var hole []string
		revealed := cardStrings(holeOnly(s.Cards))
		switch {
		case hero.SeatIndex == s.Index:
			addr, hole = hero.Address, cardStrings(hero.Hole)
			if len(hole) == 0 {
				hole = revealed
			}
		case len(revealed) > 0: // shown at showdown / all-in
			hole = revealed
		case t.IsHandInProgress && s.WasInHand && !s.IsFolded:
			for i := 0; i < t.NumHoleCards; i++ { // the chain relay masks live opponents' cards as "X"
				hole = append(hole, "X")
			}
		}
		st.Players = append(st.Players, Player{
			Address: addr, Seat: s.Index + 1, Stack: num(s.Stack),
			IsSmallBlind: t.Positions.SmallBlind != nil && *t.Positions.SmallBlind == s.Index,
			IsBigBlind:   t.Positions.BigBlind != nil && *t.Positions.BigBlind == s.Index,
			IsDealer:     t.Positions.Dealer != nil && *t.Positions.Dealer == s.Index,
			HoleCards:    hole, Status: status(s, t.IsHandInProgress), LegalActions: []any{},
			SumOfBets: num(s.Bet), Timeout: t.TurnTimeout,
		})
	}
	for i := 0; i < t.NumSeats; i++ {
		if !taken[i] {
			st.AvailableSeats = append(st.AvailableSeats, i+1)
		}
	}
	return Frame{Event: "state", GameID: strconv.FormatInt(t.ID, 10),
		Data: FrameData{GameState: st, Format: Format(t), Variant: "texas-holdem", Name: t.Name}}
}

// Decode pulls the table out of a join ack ({"table":{...}}).
func DecodeJoin(raw json.RawMessage) (BPTable, error) {
	var a struct {
		Table BPTable `json:"table"`
		Error string  `json:"error"`
		Msg   string  `json:"message"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return BPTable{}, err
	}
	if a.Error != "" {
		return BPTable{}, &UpstreamError{Code: a.Error, Message: a.Msg}
	}
	return a.Table, nil
}

type UpstreamError struct{ Code, Message string }

func (e *UpstreamError) Error() string { return e.Code + ": " + e.Message }

// board is the community cards; handEnd clears the top-level list, so fall back to the rounds.
func board(t BPTable) []Card {
	if len(t.Cards) > 0 {
		return t.Cards
	}
	var out []Card
	for _, r := range t.Rounds {
		out = append(out, r.Community...)
	}
	return out
}

// DecodeTable reads a table-shaped push (handEnd) whose fields sit at the top level.
func DecodeTable(raw json.RawMessage) (BPTable, error) {
	var t BPTable
	err := json.Unmarshal(raw, &t)
	return t, err
}
