//go:build live

package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var scrub = regexp.MustCompile(`("(?:authToken|secret|password|token)"\s*:\s*)"[^"]*"`)

// GW=http://localhost:8081 GWSECRET=... ADDR=b52test TABLE=53803 go test -tags live -run TestLiveGateway -v ./internal/gateway
// Read-only: getProfile, getMyTables, join (view). Does not sit or act.
func TestLiveGateway(t *testing.T) {
	base, secret, addr, table := os.Getenv("GW"), os.Getenv("GWSECRET"), os.Getenv("ADDR"), os.Getenv("TABLE")
	if base == "" {
		t.Skip("GW not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+base[4:]+"/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + secret}, "X-B52-Address": {addr}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(8 << 20)
	pushes := map[string]int{}
	do := func(id, ev, data string) {
		c.Write(ctx, websocket.MessageText, []byte(`{"id":"`+id+`","event":"`+ev+`","data":`+data+`}`))
		for {
			_, d, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("%s: %v", ev, err)
			}
			var o out
			json.Unmarshal(d, &o)
			if o.ID == id {
				s := scrub.ReplaceAllString(string(o.Ack), `$1"[redacted]"`)
				if len(s) > 700 {
					s = s[:700] + "…"
				}
				t.Logf("%s ack: %s", ev, s)
				return
			}
			pushes[o.Event]++
		}
	}
	do("1", "getProfile", `{}`)
	do("2", "getMyTables", `{}`)
	do("3", "join", `{"tableId":`+table+`,"password":null,"fromTable":null,"currency":null}`)
	time.Sleep(3 * time.Second)
	t.Logf("pushes seen (event: count) after join: %v", pushes)
}

// GW=... GWSECRET=... ADDR=... LIVE_EVENT=gettables LIVE_DATA='{"offset":0,"pageSize":50,"filters":{}}' go test -tags live -run TestLiveCall -v
func TestLiveCall(t *testing.T) {
	base, ev := os.Getenv("GW"), os.Getenv("LIVE_EVENT")
	if base == "" || ev == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+base[4:]+"/v1/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + os.Getenv("GWSECRET")}, "X-B52-Address": {os.Getenv("ADDR")}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(16 << 20)
	c.Write(ctx, websocket.MessageText, []byte(`{"id":"x","event":"`+ev+`","data":`+os.Getenv("LIVE_DATA")+`}`))
	for {
		_, d, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var o out
		json.Unmarshal(d, &o)
		if o.ID == "x" {
			os.Stdout.Write(append([]byte(scrub.ReplaceAllString(string(o.Ack), `$1"[redacted]"`)), '\n'))
			return
		}
	}
}
