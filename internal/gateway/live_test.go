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
