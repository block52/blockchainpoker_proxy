package adapter

import "encoding/json"

// Reveals remembers hole cards blockchain.poker has shown for the CURRENT hand, so a later
// table re-read (whose seats are masked again) does not turn them face-down mid-showdown.
// Cards arrive via two pushes: roundEnd (all-in and called) and handEnd (showdown).
type Reveals struct {
	handID int64
	bySeat map[int][]Card
}

func (r *Reveals) reset(handID int64) {
	r.handID, r.bySeat = handID, map[int][]Card{}
}

func holeOnly(cs []Card) []Card {
	var out []Card
	for _, c := range cs {
		if c.HoleCard && c.Rank != "" && c.Suit != "" {
			out = append(out, c)
		}
	}
	return out
}

// LearnTable records revealed hole cards on a table-shaped payload (join ack, handEnd).
func (r *Reveals) LearnTable(t BPTable) {
	if t.HandID != 0 && t.HandID != r.handID {
		r.reset(t.HandID)
	}
	for _, s := range t.Seats {
		if h := holeOnly(s.Cards); len(h) > 0 {
			r.bySeat[s.Index] = h
		}
	}
}

// LearnRoundEnd records an all-in reveal: {table, handId, seats:[{seatIndex, hand:[...]}]}.
func (r *Reveals) LearnRoundEnd(raw json.RawMessage) {
	var m struct {
		HandID int64 `json:"handId"`
		Seats  []struct {
			SeatIndex int    `json:"seatIndex"`
			Hand      []Card `json:"hand"`
		} `json:"seats"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	if m.HandID != 0 && m.HandID != r.handID {
		r.reset(m.HandID)
	}
	for _, s := range m.Seats {
		if h := holeOnly(s.Hand); len(h) > 0 {
			r.bySeat[s.SeatIndex] = h
		}
	}
}

// Apply fills in remembered reveals for seats that are masked in t (same hand only).
func (r *Reveals) Apply(t *BPTable) {
	if r == nil || t.HandID == 0 || t.HandID != r.handID {
		return
	}
	for i := range t.Seats {
		if len(holeOnly(t.Seats[i].Cards)) == 0 {
			if h, ok := r.bySeat[t.Seats[i].Index]; ok {
				t.Seats[i].Cards = h
			}
		}
	}
}
