module bastionzero.com/bctl/v1/bzerolib

go 1.16

replace bastionzero.com/bctl/v1/bctl => ../bctl

replace bastionzero.com/bctl/v1/bzerolib => ./

require (
	bastionzero.com/bctl/v1/bctl v0.0.0-00010101000000-000000000000
	github.com/cenkalti/backoff/v4 v4.1.1
	github.com/coreos/go-oidc/v3 v3.0.0
	github.com/gorilla/websocket v1.4.2
	github.com/rs/zerolog v1.24.0
	golang.org/x/crypto v0.0.0-20210711020723-a769d52b0f97
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	gopkg.in/tomb.v2 v2.0.0-20161208151619-d5d1b5820637
)
