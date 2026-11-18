// Package sio is a minimal Socket.IO v5 / Engine.IO v4 websocket client:
// enough to authenticate to blockchain.poker, emit with acks, and receive pushes.
package sio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var ErrClosed = errors.New("sio: connection closed")

type Client struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	ctx    context.Context

	mu     sync.Mutex
	nextID int
	acks   map[int]chan json.RawMessage
	onEvt  func(event string, data json.RawMessage)

	ready chan struct{}
	done  chan struct{}
	err   error
}

// Dial opens the websocket (wsURL like wss://host/socket.io/?EIO=4&transport=websocket),
// performs the Engine.IO open + Socket.IO connect, and starts the read loop.
// onEvent receives every server push (event name, first argument as raw JSON).
func Dial(ctx context.Context, wsURL, origin string, onEvent func(string, json.RawMessage)) (*Client, error) {
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(16 << 20)
	cctx, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, ctx: cctx, cancel: cancel, acks: map[int]chan json.RawMessage{},
		onEvt: onEvent, ready: make(chan struct{}), done: make(chan struct{})}
	go c.readLoop()
	if err := conn.Write(ctx, websocket.MessageText, []byte("40")); err != nil {
		c.Close()
		return nil, err
	}
	select {
	case <-c.ready:
		return c, nil
	case <-c.done:
		return nil, c.errOr(ErrClosed)
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	}
}

func (c *Client) errOr(d error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return d
}

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Close() {
	c.cancel()
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

// Emit sends an event and waits for the server ack. Returns the ack's first argument.
func (c *Client) Emit(ctx context.Context, event string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal([]any{event, payload})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.acks[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.acks, id); c.mu.Unlock() }()

	frame := "42" + strconv.Itoa(id) + string(body)
	if err := c.conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		return nil, err
	}
	select {
	case first := <-ch:
		return first, nil
	case <-c.done:
		return nil, c.errOr(ErrClosed)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("sio: ack timeout for %q", event)
	}
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		typ, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			return
		}
		if typ != websocket.MessageText || len(data) == 0 {
			continue
		}
		s := string(data)
		switch s[0] {
		case '2': // ping -> pong
			_ = c.conn.Write(c.ctx, websocket.MessageText, []byte("3"))
		case '4':
			c.handleSIO(s[1:])
		}
	}
}

func (c *Client) handleSIO(s string) {
	if s == "" {
		return
	}
	t, rest := s[0], s[1:]
	if strings.HasPrefix(rest, "/") { // namespace prefix
		if i := strings.IndexByte(rest, ','); i >= 0 {
			rest = rest[i+1:]
		}
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	idStr, payload := rest[:i], rest[i:]
	switch t {
	case '0': // CONNECT
		select {
		case <-c.ready:
		default:
			close(c.ready)
		}
	case '2': // EVENT
		var arr []json.RawMessage
		if json.Unmarshal([]byte(payload), &arr) != nil || len(arr) == 0 {
			return
		}
		var ev string
		_ = json.Unmarshal(arr[0], &ev)
		var arg json.RawMessage
		if len(arr) > 1 {
			arg = arr[1]
		}
		if c.onEvt != nil {
			c.onEvt(ev, arg)
		}
	case '3': // ACK
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return
		}
		var arr []json.RawMessage
		first := json.RawMessage("null")
		if json.Unmarshal([]byte(payload), &arr) == nil && len(arr) > 0 {
			first = arr[0]
		}
		c.mu.Lock()
		ch := c.acks[id]
		c.mu.Unlock()
		if ch != nil {
			ch <- first
		}
	case '1', '4': // DISCONNECT / CONNECT_ERROR
		c.mu.Lock()
		c.err = fmt.Errorf("sio: server closed namespace: %s", s)
		c.mu.Unlock()
		c.cancel()
	}
}
