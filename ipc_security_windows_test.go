//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestIPCAnonymousClientCannotMutate(t *testing.T) {
	s := privateIPCServer(t)
	s.proxy = &testEngineProxy{}
	s.ring.Add(DNSRequestEntry{Domain: "preserved.example", Type: "A"})
	for _, op := range []string{"set_upstream", " CLEAR_LOG "} {
		t.Run(op, func(t *testing.T) {
			name, _ := windows.UTF16PtrFromString(s.pipeName)
			var h windows.Handle
			var err error
			for deadline := time.Now().Add(time.Second); ; {
				h, err = windows.CreateFile(name, ipcClientAccess, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			f := os.NewFile(uintptr(h), "anonymous IPC client")
			defer f.Close()
			if err = f.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(dnsIPCRequest{Op: op, Upstreams: []Upstream{PredefinedUpstream(ProviderNextDNS)}})
			if _, err = f.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(f).ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var resp dnsIPCResponse
			if err = json.Unmarshal(line, &resp); err != nil {
				t.Fatal(err)
			}
			if resp.OK || !strings.Contains(resp.Error, "Administrator") {
				t.Fatalf("anonymous mutation was not rejected: %+v", resp)
			}
		})
	}
	rows, _ := s.ring.Snapshot()
	if len(rows) != 1 || len(s.proxy.(*testEngineProxy).snapshot()) != 0 {
		t.Fatal("rejected request changed service state")
	}
	// Authentication must always revert its thread identity, including failures.
	if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "clear_log"}); err != nil {
		t.Fatalf("authorized client failed after anonymous requests: %v", err)
	}
}

func TestIPCRestrictedClientCannotMutateOrCreateServer(t *testing.T) {
	s := privateIPCServer(t)
	if !s.elevated {
		t.Skip("server-creation ACL test requires an elevated server")
	}
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	disabled := windows.SIDAndAttributes{Sid: admins}
	var restricted windows.Token
	createRestricted := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	ok, _, callErr := createRestricted.Call(uintptr(source), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err = windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_ADJUST_DEFAULT, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	medium, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		t.Fatal(err)
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err = windows.SetTokenInformation(impersonation, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = windows.SetThreadToken(nil, impersonation); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			panic(err)
		}
	}()
	if _, err = callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatalf("restricted interactive user cannot read state: %v", err)
	}
	if _, err = callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "clear_log"}); err == nil {
		t.Fatal("restricted client changed an elevated runtime")
	} else {
		var rejection *ipcResponseError
		if !errors.As(err, &rejection) || !strings.Contains(err.Error(), "Administrator") {
			t.Fatalf("mutation failed for a reason other than authorization: %v", err)
		}
	}
	name, _ := windows.UTF16PtrFromString(s.pipeName)
	h, err := createDNSPipe(name, nil, 0)
	if err == nil {
		_ = windows.CloseHandle(h)
		t.Fatal("restricted client created a server instance")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("unexpected instance creation error: %v", err)
	}
}

func TestIPCClientRejectsOversizedResponse(t *testing.T) {
	s := privateIPCServer(t)
	for i := 0; i < maxDNSLogRows; i++ {
		s.ring.Add(DNSRequestEntry{Domain: strings.Repeat("x", ipcMaxResponseBytes/maxDNSLogRows+100)})
	}
	if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err == nil || !strings.Contains(err.Error(), "response exceeds size limit") {
		t.Fatalf("oversized response was not bounded: %v", err)
	}
}

func TestIPCPipeReservationAndStartupErrors(t *testing.T) {
	s := privateIPCServer(t)
	for i := 0; i < 20; i++ {
		if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
			t.Fatal(err)
		}
		other := newDNSIPCServer(NewDNSRequestRing(), nil)
		other.pipeName = s.pipeName
		if err := other.start(); err == nil {
			other.close()
			t.Fatal("second server took over a reserved pipe")
		}
		other.close() // Failed startup still supports safe cleanup.
	}
	invalid := newDNSIPCServer(NewDNSRequestRing(), nil)
	invalid.pipeName = "invalid\x00name"
	if err := invalid.start(); err == nil {
		t.Fatal("invalid pipe name was accepted")
	}
	invalid.close()
}
