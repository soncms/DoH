//go:build !windows

package service

import appconfig "sondns/internal/config"

import "log"

type DPIBackend struct{}

func NewDPIBackend(appconfig.Config, *log.Logger) (*DPIBackend, error) { return nil, nil }
func (*DPIBackend) Start() error                                       { return nil }
func (*DPIBackend) Shutdown()                                          {}
