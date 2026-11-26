module github.com/mikaclements/voice-guide

go 1.27.3

// npm installs JavaScript dependencies here; Go tools never need to look inside.
ignore ./node_modules

require (
	github.com/coder/websocket v1.8.15
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/jackc/pgx/v5 v5.13.0
	github.com/pion/ice/v4 v4.4.9
	github.com/pion/interceptor v0.1.51
	github.com/pion/opus v0.1.0
	github.com/pion/rtp v1.10.6
	github.com/pion/webrtc/v4 v4.2.29
	github.com/pressly/goose/v3 v3.30.1
	github.com/zserge/govad v0.0.0-20260330155402-74750eabf3a4
	go.yaml.in/yaml/v3 v3.0.6
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/pion/datachannel v1.6.4 // indirect
	github.com/pion/dtls/v3 v3.1.16 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/sctp v1.12.1 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/stun/v4 v4.0.1 // indirect
	github.com/pion/transport/v5 v5.1.5 // indirect
	github.com/pion/turn/v5 v5.1.3 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/telemetry v0.0.0-20261009151841-97e8ea77aa2a // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

tool golang.org/x/tools/cmd/deadcode
