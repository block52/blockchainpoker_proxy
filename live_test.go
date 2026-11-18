//go:build live

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Run: go run . -listen :8099 -public http://localhost:8099 &  then
//
//	PROXY=http://localhost:8099 go test -tags live -run TestLiveWS -v
//
// Unauthenticated: reads the Engine.IO open packet and a socket.io connect, nothing more.
func TestLiveWS(t *testing.T) {
	base := os.Getenv("PROXY")
	if base == "" {
		base = "http://localhost:8099"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hdr := map[string][]string{"Origin": {base}}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/socket.io/?EIO=4&transport=websocket", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, d, err := c.Read(ctx)
	if err != nil || !strings.HasPrefix(string(d), "0{") {
		t.Fatalf("open: %q %v", d, err)
	}
	t.Logf("open: %s", d)
	if err := c.Write(ctx, websocket.MessageText, []byte("40")); err != nil {
		t.Fatal(err)
	}
	_, d, err = c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after connect: %s", d)
}
