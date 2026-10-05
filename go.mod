module sondns

go 1.27.1

require (
	github.com/AdguardTeam/dnsproxy v0.84.2
	github.com/ameshkov/dnsstamps v1.0.3
	github.com/lxn/walk v0.0.0-20210112085537-c389da54e794
	github.com/lxn/win v0.0.0-20210218163916-a377121e959e
	github.com/miekg/dns v1.1.73
	golang.org/x/sys v0.48.0
)

require (
	github.com/AdguardTeam/dnscrypt v0.0.2 // indirect
	github.com/AdguardTeam/golibs v0.35.13 // indirect
	github.com/bluele/gcache v0.0.2 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.60.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
	gopkg.in/Knetic/govaluate.v3 v3.0.0 // indirect
)

replace github.com/lxn/walk => ./third_party/walk
