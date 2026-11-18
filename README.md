# blockchainpoker_proxy (POC, Stage 0)

Passthrough proxy for https://blockchain.poker. Serves their front end from our origin and
relays their socket.io websocket to their backend, recording every frame for protocol discovery.

```
go run . -listen :8080 -public http://localhost:8080
# open http://localhost:8080/?table=53483
```

| Flag | Default | |
|---|---|---|
| `-api` | `https://api.blockchain.poker` | upstream socket.io API |
| `-web` | `https://blockchain.poker` | upstream static front end |
| `-public` | `http://localhost:8080` | URL browsers use to reach us (baked into rewritten bundles) |
| `-record` | `captures/frames.jsonl` | JSONL capture, `''` disables |
| `-log-pings` | false | include engine.io ping/pong |

## How it works

- `/socket.io/*` websocket: frame-for-frame relay (Engine.IO v4 / Socket.IO v5 are opaque to us). Origin is set to the upstream web origin.
- `/socket.io/*` polling and `socket.io.js`: plain reverse proxy.
- Everything else: reverse proxy to `-web`; HTML/JS/JSON bodies have `https://api.blockchain.poker` / `wss://...` rewritten to `-public`.
- Capture lines label events (`gettables`) and correlate acks (`gettables#ack`) via ack ids.
  `authToken`, `secret`, `password`, `mfaToken`, `trustedDeviceToken`, `captchaToken` are redacted before writing.

## Tests

`go test ./...` uses a fake upstream. Live relay check (unauthenticated): run the proxy, then
`PROXY=http://localhost:8099 go test -tags live -run TestLiveWS -v`.

## Stage 1 (next)

Replace the upstream relay with a Go socket.io server backed by our engine for the play path
(`authenticate`, `gettables`, `join`, `sit`, `stand`, `topUp`, `getMinBuyIn`, action events, chat), stubbing the rest.
Payload shapes come from `captures/frames.jsonl`.
