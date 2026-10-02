//go:build !windows

package main

import "log"

type DPIBackend struct{}

func NewDPIBackend(Config, *log.Logger) (*DPIBackend, error) { return nil, nil }
func (*DPIBackend) Start() error                             { return nil }
func (*DPIBackend) Shutdown()                                {}
