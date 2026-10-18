//go:build !windows

package control

import appconfig "sondns/internal/config"
import requestlog "sondns/internal/requestlog"

import (
	"context"
	"errors"
)

type Server struct{}

type Engine interface {
	SetUpstreams(context.Context, []appconfig.Upstream) error
}

func NewServer(*requestlog.Ring, Engine) *Server {
	return &Server{}
}
func (*Server) Start() error { return nil }
func (*Server) Close()       {}

type Request struct {
	Op        string               `json:"op"`
	Version   uint64               `json:"version,omitempty"`
	Upstreams []appconfig.Upstream `json:"upstreams,omitempty"`
}
type Response struct {
	OK              bool               `json:"ok"`
	SchemaVersion   int                `json:"schema_version,omitempty"`
	Version         uint64             `json:"version,omitempty"`
	OverwrittenRows uint64             `json:"overwritten_rows,omitempty"`
	Entries         []requestlog.Entry `json:"entries,omitempty"`
	Error           string             `json:"error,omitempty"`
}

func Call(Request) (Response, error) {
	return Response{}, errors.New("SonDNS IPC is only available on Windows")
}
