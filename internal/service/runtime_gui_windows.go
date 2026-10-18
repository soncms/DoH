//go:build windows && !console

package service

import (
	"log"

	appconfig "sondns/internal/config"
	dnsengine "sondns/internal/engine"
)

const ConsoleBuild = false

func ConsoleCLI([]string) error { return nil }

func portableRuntimeDir() (string, error) { return "", nil }

func newRuntimeEngine(c appconfig.Config, logger *log.Logger) (runtimeEngine, *dnsengine.Resolver, error) {
	return dnsengine.New(c, logger)
}
