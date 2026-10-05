//go:build windows

package main

import "testing"

func TestQuoteWindowsArgument(t *testing.T) {
	tests := map[string]string{
		`C:\ProgramData\SonDNS\sondns.json`:   `C:\ProgramData\SonDNS\sondns.json`,
		`C:\Program Files\SonDNS\sondns.json`: `"C:\Program Files\SonDNS\sondns.json"`,
		`C:\SonDNS Folder\`:                   `"C:\SonDNS Folder\\"`,
		`a"b`:                                 `"a\"b"`,
	}
	for input, want := range tests {
		if got := quoteWindowsArgument(input); got != want {
			t.Fatalf("quoteWindowsArgument(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestServiceConfigPath(t *testing.T) {
	tests := map[string]string{
		`"C:\Program Files\SonDNS\SonDNS.exe" run --config="C:\ProgramData\SonDNS\sondns.json"`: `C:\ProgramData\SonDNS\sondns.json`,
		`C:\SonDNS\SonDNS.exe run --config=C:\Data\sondns.json`:                                 `C:\Data\sondns.json`,
		`C:\SonDNS\SonDNS.exe run --config C:\Data\sondns.json`:                                 `C:\Data\sondns.json`,
	}
	for input, want := range tests {
		if got := serviceConfigPath(input); got != want {
			t.Fatalf("serviceConfigPath(%q) = %q, want %q", input, got, want)
		}
	}
}
