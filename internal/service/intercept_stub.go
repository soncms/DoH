//go:build !windows

package service

import appconfig "sondns/internal/config"
import dnsengine "sondns/internal/engine"

import (
	"errors"
	"log"
)

type Interceptor struct{}

func NewInterceptor(appconfig.Config, *dnsengine.Resolver, *log.Logger) (*Interceptor, error) {
	return nil, errors.New("windivert interception is only supported on Windows")
}
func (*Interceptor) Start() error {
	return errors.New("windivert interception is only supported on Windows")
}
func (*Interceptor) Shutdown() {}

func cleanupWinDivertDriver() error { return nil }
