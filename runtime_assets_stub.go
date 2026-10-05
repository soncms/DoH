//go:build !windows

package main

type runtimeAssetDiagnostic struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

func runtimeAssetDiagnostics() []runtimeAssetDiagnostic { return nil }
