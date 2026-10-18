//go:build !windows

package service

type processMutex struct{}

func acquireProcessMutex(string) (*processMutex, bool, error) { return &processMutex{}, true, nil }
func (*processMutex) Close()                                  {}
