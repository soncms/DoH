//go:build windows && !console

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var lifecycleNetAPI = windows.NewLazySystemDLL("netapi32.dll")
var lifecycleNetUserAdd = lifecycleNetAPI.NewProc("NetUserAdd")
var lifecycleNetUserDel = lifecycleNetAPI.NewProc("NetUserDel")
var lifecycleNetUserGetInfo = lifecycleNetAPI.NewProc("NetUserGetInfo")
var lifecycleNetGroupAdd = lifecycleNetAPI.NewProc("NetLocalGroupAddMembers")
var lifecycleNetBufferFree = lifecycleNetAPI.NewProc("NetApiBufferFree")
var lifecycleLogonUser = windows.NewLazySystemDLL("advapi32.dll").NewProc("LogonUserW")
var lifecycleCreateWithToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")

type lifecycleAccountReport struct {
	Name, SID                                                        string
	Elevated, Administrator, ConfigRead, SCMRead, StateRead, LogRead bool
	ConfigWriteDenied, SCMMutateDenied, PipeCreateDenied             uint32
	RejectedOperations                                               []string
	ConfigWriteAllowed, SCMMutateAllowed, LogClearAllowed, Removed   bool
}

type lifecycleAccountsReport struct {
	CreatorSID, ConfigSHA256, ChildSID, HandoffError         string
	Standard, Administrator                                  lifecycleAccountReport
	ChildPID                                                 uint32
	HandoffWait                                              time.Duration
	StandardVerified, AdministratorVerified, HandoffVerified bool
	ChildExited, AccountsRemoved                             bool
}

type lifecycleAccount struct {
	t                      *testing.T
	name                   string
	primary, impersonation windows.Token
	report                 *lifecycleAccountReport
	removed                bool
}

// Account creation is deliberately absent from normal unit/UI test runs.
// Only the opted-in lifecycle on an acknowledged clean disposable VM
// calls this helper. No user rights/UAC policies or existing accounts are changed.
func newLifecycleAccount(t *testing.T, admin bool, report *lifecycleAccountReport) *lifecycleAccount {
	t.Helper()
	if os.Getenv("SONDNS_ACCOUNT_TEST") != "1" || os.Getenv("SONDNS_SERVICE_TEST") != "1" {
		t.Fatal("temporary accounts require the explicitly opted-in disposable VM runner")
	}
	a := &lifecycleAccount{t: t, name: "SDt_" + rand.Text()[:12], report: report}
	name, _ := windows.UTF16PtrFromString(a.name)
	password, _ := windows.UTF16FromString("Aa1!" + rand.Text() + rand.Text())
	defer clear(password)
	var info struct {
		Name, Password         *uint16
		PasswordAge, Privilege uint32
		HomeDir, Comment       *uint16
		Flags                  uint32
		ScriptPath             *uint16
	}
	info.Name, info.Password = name, &password[0]
	info.Privilege, info.Flags = 1, 0x1|0x200|0x10000 // USER_PRIV_USER, SCRIPT/NORMAL/DONT_EXPIRE
	var parameter uint32
	status, _, _ := lifecycleNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&parameter)))
	if status != 0 {
		t.Fatalf("create owned temporary Windows account: status=%d parameter=%d", status, parameter)
	}
	t.Cleanup(a.cleanup) // Also covers a failed logon/group setup below.
	report.Name = a.name
	groups := []windows.WELL_KNOWN_SID_TYPE{windows.WinBuiltinUsersSid}
	if admin {
		groups = append(groups, windows.WinBuiltinAdministratorsSid)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	member, _ := windows.UTF16PtrFromString(host + `\` + a.name)
	for _, kind := range groups {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil {
			t.Fatal(err)
		}
		group, _, _, err := sid.LookupAccount("")
		if err != nil {
			t.Fatal(err)
		}
		groupName, _ := windows.UTF16PtrFromString(group)
		entry := struct{ Name *uint16 }{member} // LOCALGROUP_MEMBERS_INFO_3
		status, _, _ = lifecycleNetGroupAdd.Call(0, uintptr(unsafe.Pointer(groupName)), 3, uintptr(unsafe.Pointer(&entry)), 1)
		if status != 0 && status != 1378 { // Already in the Users alias is harmless.
			t.Fatalf("add owned account to local group: status=%d", status)
		}
	}
	domain, _ := windows.UTF16PtrFromString(".")
	ok, _, err := lifecycleLogonUser.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(domain)),
		uintptr(unsafe.Pointer(&password[0])), 2 /* INTERACTIVE */, 0, uintptr(unsafe.Pointer(&a.primary)))
	if ok == 0 {
		t.Fatalf("log on owned temporary account: %v", err)
	}
	if admin && !a.primary.IsElevated() {
		linked, err := a.primary.GetLinkedToken()
		if err != nil {
			t.Fatalf("get owned administrator's linked token: %v", err)
		}
		_ = a.primary.Close()
		a.primary = linked
	}
	if err := windows.DuplicateTokenEx(a.primary, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE,
		nil, windows.SecurityImpersonation, windows.TokenImpersonation, &a.impersonation); err != nil {
		t.Fatal(err)
	}
	user, err := a.primary.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	report.SID, report.Elevated = user.User.Sid.String(), a.primary.IsElevated()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	report.Administrator, err = a.impersonation.IsMember(admins)
	if err != nil || report.Elevated != admin || report.Administrator != admin {
		t.Fatalf("owned account token role differs: elevated=%t administrator=%t want=%t error=%v",
			report.Elevated, report.Administrator, admin, err)
	}
	return a
}

func (a *lifecycleAccount) cleanup() {
	if a.removed {
		return
	}
	for _, token := range []*windows.Token{&a.impersonation, &a.primary} {
		if *token != 0 {
			_ = token.Close()
			*token = 0
		}
	}
	name, _ := windows.UTF16PtrFromString(a.name)
	status, _, _ := lifecycleNetUserDel.Call(0, uintptr(unsafe.Pointer(name)))
	if status != 0 && status != 2221 /* NERR_UserNotFound */ {
		a.t.Errorf("delete owned temporary account: status=%d", status)
		return
	}
	var buffer uintptr
	status, _, _ = lifecycleNetUserGetInfo.Call(0, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&buffer)))
	if buffer != 0 {
		lifecycleNetBufferFree.Call(buffer)
	}
	if status != 2221 {
		a.t.Errorf("owned temporary account removal not verified: status=%d", status)
		return
	}
	a.removed, a.report.Removed = true, true
}

func (a *lifecycleAccount) as(body func()) {
	a.t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, a.impersonation); err != nil {
		a.t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			panic(fmt.Sprintf("account test RevertToSelf: %v", err))
		}
	}()
	body()
}

func lifecycleConfigWriteAccess(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		return windows.CloseHandle(h) // Permission probe only: never write config bytes.
	}
	return err
}

func lifecycleSCMAccess(access uint32) error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, access)
	if err == nil {
		return windows.CloseServiceHandle(h) // Never send a service control in a role probe.
	}
	return err
}

func runLifecycleAccountAccess(t *testing.T, configPath string, settings Config) {
	t.Helper()
	report := lifecycleAccountsReport{}
	var accounts []*lifecycleAccount
	defer func() {
		for i := len(accounts) - 1; i >= 0; i-- {
			accounts[i].cleanup()
		}
		report.AccountsRemoved = len(accounts) == 2 && report.Standard.Removed && report.Administrator.Removed
		if report.AccountsRemoved && report.ChildExited {
			t.Log("temporary Windows accounts removed and owned child exited")
		}
		if destination := os.Getenv("SONDNS_SERVICE_TEST_LOG"); destination != "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err == nil {
				err = os.WriteFile(filepath.Join(filepath.Dir(destination), "accounts.json"), append(data, '\n'), 0o600)
			}
			if err != nil {
				t.Errorf("save account verification report: %v", err)
			}
		}
	}()
	creator, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	report.CreatorSID = creator.User.Sid.String()
	standard := newLifecycleAccount(t, false, &report.Standard)
	accounts = append(accounts, standard)
	admin := newLifecycleAccount(t, true, &report.Administrator)
	accounts = append(accounts, admin)
	if report.Standard.SID == report.Administrator.SID || report.CreatorSID == report.Standard.SID || report.CreatorSID == report.Administrator.SID {
		t.Fatal("account fixture did not create three distinct Windows identities")
	}
	before := readLifecycleMigrationFile(t, configPath)
	report.ConfigSHA256 = migrationHash(before)
	// Seed a real service log row, then track its ID rather than assuming the
	// hosted OS produces no background DNS while the account tests run.
	requireLifecycleMigrationDNS(t, settings.Listen)
	var canary uint64
	for deadline := time.Now().Add(3 * time.Second); canary == 0 && time.Now().Before(deadline); {
		response, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range response.Entries {
			if row.Domain == "sondns-test.invalid" {
				canary = max(canary, row.ID)
			}
		}
		if canary == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if canary == 0 {
		t.Fatal("real service log canary missing")
	}
	standard.as(func() {
		readLifecycleAccountAccess(t, configPath, before, &report.Standard)
		for _, probe := range []struct {
			label string
			call  func() error
			code  *uint32
		}{
			{"config write", func() error { return lifecycleConfigWriteAccess(configPath) }, &report.Standard.ConfigWriteDenied},
			{"SCM mutation", func() error { return lifecycleSCMAccess(windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_STOP) }, &report.Standard.SCMMutateDenied},
			{"pipe server creation", func() error {
				name, _ := windows.UTF16PtrFromString(sondnsPipeName)
				h, err := createDNSPipe(name, nil, 0)
				if err == nil {
					_ = windows.CloseHandle(h)
				}
				return err
			}, &report.Standard.PipeCreateDenied},
		} {
			err := probe.call()
			if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("standard account %s was not denied by Windows: %v", probe.label, err)
			}
			*probe.code = uint32(windows.ERROR_ACCESS_DENIED)
		}
		for _, op := range []string{" CLEAR_LOG ", " SET_UPSTREAM "} {
			_, err := callDNSIPC(dnsIPCRequest{Op: op, Upstreams: settings.Upstreams})
			var rejected *ipcResponseError
			if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "Administrator") {
				t.Fatalf("standard-account IPC mutation did not receive authorization rejection: %v", err)
			}
			report.Standard.RejectedOperations = append(report.Standard.RejectedOperations, op)
		}
	})
	if !bytes.Equal(before, readLifecycleMigrationFile(t, configPath)) || !lifecycleLogHasID(t, canary) {
		t.Fatal("rejected standard-account requests changed saved config or cleared the log canary")
	}
	report.StandardVerified = true
	t.Log("cross-account access verified: standard monitoring allowed; config, SCM and IPC mutations denied")
	admin.as(func() {
		readLifecycleAccountAccess(t, configPath, before, &report.Administrator)
		if err := lifecycleConfigWriteAccess(configPath); err != nil {
			t.Fatal(err)
		}
		report.Administrator.ConfigWriteAllowed = true
		if err := lifecycleSCMAccess(windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_STOP); err != nil {
			t.Fatal(err)
		}
		report.Administrator.SCMMutateAllowed = true
		if _, err := callDNSIPC(dnsIPCRequest{Op: "clear_log"}); err != nil {
			t.Fatalf("separate elevated administrator cannot clear real service log: %v", err)
		}
		report.Administrator.LogClearAllowed = true
	})
	if lifecycleLogHasID(t, canary) || !bytes.Equal(before, readLifecycleMigrationFile(t, configPath)) {
		t.Fatal("authorized log clear retained old row or rewrote saved configuration")
	}
	report.AdministratorVerified = true
	t.Log("cross-account administrator verified: protected access and authorized IPC log clear")
	requireLifecycleMigrationDNS(t, settings.Listen)
	runLifecycleAccountHandoff(t, standard, admin, filepath.Dir(configPath), &report)
}

func readLifecycleAccountAccess(t *testing.T, path string, want []byte, report *lifecycleAccountReport) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("real account cannot read exact portable config: %v", err)
	}
	report.ConfigRead = true
	if err := lifecycleSCMAccess(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS); err != nil {
		t.Fatalf("real account cannot query the installed service: %v", err)
	}
	report.SCMRead = true
	for _, op := range []string{"state", "snapshot"} {
		response, err := callDNSIPC(dnsIPCRequest{Op: op})
		if err != nil || !response.OK || response.SchemaVersion != 2 {
			t.Fatalf("real account cannot read IPC %s: %+v %v", op, response, err)
		}
	}
	report.StateRead, report.LogRead = true, true
}

func lifecycleLogHasID(t *testing.T, id uint64) bool {
	t.Helper()
	response, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range response.Entries {
		if row.ID == id {
			return true
		}
	}
	return false
}

func runLifecycleAccountHandoff(t *testing.T, standard, admin *lifecycleAccount, cwd string, report *lifecycleAccountsReport) {
	t.Helper()
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	application, _ := windows.UTF16PtrFromString(exe)
	command, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{exe, "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 2"}))
	directory, _ := windows.UTF16PtrFromString(cwd)
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	var child windows.ProcessInformation
	ok, _, err := lifecycleCreateWithToken.Call(uintptr(standard.primary), 0, uintptr(unsafe.Pointer(application)), uintptr(unsafe.Pointer(command)),
		windows.CREATE_NO_WINDOW, 0, uintptr(unsafe.Pointer(directory)), uintptr(unsafe.Pointer(&startup)), uintptr(unsafe.Pointer(&child)))
	if ok == 0 {
		t.Fatalf("start owned standard-account wait fixture: %v", err)
	}
	_ = windows.CloseHandle(child.Thread)
	defer func() {
		state, err := windows.WaitForSingleObject(child.Process, 0)
		if err == nil && state == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(child.Process, 1)
			state, err = windows.WaitForSingleObject(child.Process, 5000)
		}
		report.ChildExited = err == nil && state == windows.WAIT_OBJECT_0
		if !report.ChildExited {
			t.Errorf("owned account child remains: wait=%d error=%v", state, err)
		}
		_ = windows.CloseHandle(child.Process)
	}()
	report.ChildPID = child.ProcessId
	var token windows.Token
	if err := windows.OpenProcessToken(child.Process, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	user, err := token.GetTokenUser()
	_ = token.Close()
	if err != nil || user.User.Sid.String() != standard.report.SID {
		t.Fatalf("wait child identity differs: %v", err)
	}
	report.ChildSID = user.User.Sid.String()
	state, err := windows.WaitForSingleObject(child.Process, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("account wait fixture already exited: %d %v", state, err)
	}
	admin.as(func() {
		started := time.Now()
		err = waitForGUIHandoff(child.ProcessId)
		report.HandoffWait = time.Since(started)
		if err != nil {
			report.HandoffError = err.Error()
			t.Fatalf("separate-account administrator handoff wait: %v", err)
		}
	})
	state, err = windows.WaitForSingleObject(child.Process, 0)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("handoff returned before owned child exit: %d %v", state, err)
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(child.Process, &exit); err != nil || exit != 0 {
		t.Fatalf("account child did not exit normally: %d %v", exit, err)
	}
	report.HandoffVerified = true
	t.Log("cross-account handoff wait verified: separate administrator waited for the live standard-user process")
}
