// blockchainpoker_proxy: POC passthrough for blockchain.poker.
//
// Serves their front end from our origin (API origin rewritten to us), relays the
// socket.io websocket frame-by-frame to their backend, and records every frame to
// JSONL (credentials redacted) so the protocol can be reimplemented in Stage 1.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/block52/blockchainpoker_proxy/internal/gateway"
)

func main() {
	var cfg Config
	var record string
	var pings bool
	flag.StringVar(&cfg.Listen, "listen", ":8080", "listen address")
	flag.StringVar(&cfg.APIUpstream, "api", "https://api.blockchain.poker", "upstream socket.io API")
	flag.StringVar(&cfg.WebUpstream, "web", "https://blockchain.poker", "upstream static front end")
	flag.StringVar(&cfg.PublicURL, "public", "http://localhost:8080", "URL browsers use to reach this proxy")
	flag.StringVar(&record, "record", "captures/frames.jsonl", "JSONL frame capture path ('' disables)")
	flag.BoolVar(&pings, "log-pings", false, "also record engine.io ping/pong frames")
	var gwStore, gwOrigins string
	var gwOn bool
	flag.BoolVar(&gwOn, "gateway", false, "enable the b52 gateway at /v1/ws (needs B52GW_KEY, B52GW_DEV_SECRET)")
	flag.StringVar(&gwStore, "gateway-store", "captures/creds.enc", "encrypted credential store for per-user bp accounts")
	flag.StringVar(&gwOrigins, "gateway-origins", "localhost:*", "comma-separated allowed browser origins for /v1/ws")
	flag.Parse()

	var out io.Writer
	if record != "" {
		if err := os.MkdirAll(dirOf(record), 0o755); err != nil {
			log.Fatal(err)
		}
		f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	var rec *Recorder
	if out != nil {
		rec = NewRecorder(out, pings)
	}
	srv, err := NewServer(cfg, rec)
	if err != nil {
		log.Fatal(err)
	}
	if gwOn {
		// Secrets come from the environment, never flags (shell history / ps).
		st, err := gateway.NewFileStore(gwStore, os.Getenv("B52GW_KEY"))
		if err != nil {
			log.Fatal(err)
		}
		if os.Getenv("B52GW_DEV_SECRET") == "" {
			log.Fatal("B52GW_DEV_SECRET is required (dev auth stand-in until wallet-signature auth lands)")
		}
		up := "ws" + strings.TrimPrefix(cfg.APIUpstream, "http") + "/socket.io/?EIO=4&transport=websocket"
		srv.GW = gateway.New(gateway.Config{
			UpstreamWS:     up,
			UpstreamOrigin: cfg.WebUpstream,
			CaptchaToken:   os.Getenv("BP_CAPTCHA_BYPASS"),
			TestUsername:   os.Getenv("BP_TEST_USERNAME"),
			TestPassword:   os.Getenv("BP_TEST_PASSWORD"),
			AllowedOrigins: strings.Split(gwOrigins, ","),
		}, gateway.DevAuth{Secret: os.Getenv("B52GW_DEV_SECRET")}, st)
		log.Printf("gateway enabled at /v1/ws (store %s, provisioning %v, test account %v)", gwStore, os.Getenv("BP_CAPTCHA_BYPASS") != "", os.Getenv("BP_TEST_USERNAME") != "")
	}
	log.Printf("listening %s, public %s, api %s, web %s, record %q", cfg.Listen, cfg.PublicURL, cfg.APIUpstream, cfg.WebUpstream, record)
	hs := &http.Server{Addr: cfg.Listen, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(hs.ListenAndServe())
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
