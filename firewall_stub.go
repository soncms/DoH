//go:build !windows

package main

type FirewallManager struct{}

func (FirewallManager) BlockApp(string) error { return nil }
func (FirewallManager) AllowApp(string) error { return nil }
func (FirewallManager) RemoveAll() error      { return nil }
