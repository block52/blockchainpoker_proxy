package main

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// secretRe masks credential-bearing JSON fields before anything is written to disk.
var secretRe = regexp.MustCompile(`("(?:authToken|secret|password|oldPassword|newPassword|mfaToken|trustedDeviceToken|captchaToken|token)"\s*:\s*)"[^"]*"`)

func redact(s string) string { return secretRe.ReplaceAllString(s, `$1"[redacted]"`) }

// Entry is one recorded websocket frame (JSONL line).
type Entry struct {
	Time  time.Time `json:"t"`
	Sess  string    `json:"sid"`
	Dir   string    `json:"dir"` // c2s | s2c
	Kind  string    `json:"kind"`
	Event string    `json:"event,omitempty"` // socket.io event name; "<name>#ack" for acks
	Ack   string    `json:"ack,omitempty"`
	Len   int       `json:"len"`
	Data  string    `json:"data,omitempty"`
}

type Recorder struct {
	mu        sync.Mutex
	enc       *json.Encoder
	logPings  bool
	ackEvents map[string]string // sid+":"+ackID -> event, so acks are labelled
}

func NewRecorder(w io.Writer, logPings bool) *Recorder {
	return &Recorder{enc: json.NewEncoder(w), logPings: logPings, ackEvents: map[string]string{}}
}

// parseFrame understands just enough Engine.IO v4 / Socket.IO v5 framing to label events.
// Text frame: <eio type><sio type>[/ns,][ackId]<json>. e.g. 42["gettables",{...}] or 431[{...}].
func parseFrame(s string) (eio, sio byte, ackID, event string) {
	if s == "" {
		return
	}
	eio = s[0]
	if eio != '4' || len(s) < 2 {
		return
	}
	sio = s[1]
	rest := s[2:]
	if strings.HasPrefix(rest, "/") {
		if i := strings.IndexByte(rest, ','); i >= 0 {
			rest = rest[i+1:]
		}
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	ackID, rest = rest[:i], rest[i:]
	if sio == '2' { // EVENT: ["name", ...]
		var arr []json.RawMessage
		if json.Unmarshal([]byte(rest), &arr) == nil && len(arr) > 0 {
			_ = json.Unmarshal(arr[0], &event)
		}
	}
	return
}

func (r *Recorder) Frame(sid, dir string, binary bool, data []byte) {
	if r == nil {
		return
	}
	e := Entry{Time: time.Now().UTC(), Sess: sid, Dir: dir, Len: len(data)}
	if binary {
		e.Kind = "binary"
		r.write(e)
		return
	}
	s := string(data)
	eio, sio, ack, event := parseFrame(s)
	switch eio {
	case '2', '3':
		if !r.logPings {
			return
		}
		e.Kind = "ping"
	case '0':
		e.Kind = "open"
	default:
		e.Kind = "text"
	}
	r.mu.Lock()
	key := sid + ":" + ack
	switch {
	case eio == '4' && sio == '2' && ack != "" && dir == "c2s":
		r.ackEvents[key] = event
	case eio == '4' && sio == '3' && dir == "s2c":
		if ev, ok := r.ackEvents[key]; ok {
			event = ev + "#ack"
			delete(r.ackEvents, key)
		}
	}
	r.mu.Unlock()
	e.Event, e.Ack, e.Data = event, ack, redact(s)
	r.write(e)
}

func (r *Recorder) write(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.enc.Encode(e)
}
