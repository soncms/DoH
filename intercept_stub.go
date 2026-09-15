//go:build !windows

package main

import (
	"errors"
	"log"
)

type Interceptor struct{}

func NewInterceptor(Config, *Resolver, *log.Logger) (*Interceptor, error) {
	return nil, errors.New("windivert interception is only supported on Windows")
}
func (*Interceptor) Start() error {
	return errors.New("windivert interception is only supported on Windows")
}
func (*Interceptor) Shutdown() {}

func cleanupWinDivertDriver() error { return nil }
