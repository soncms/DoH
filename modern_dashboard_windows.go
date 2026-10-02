//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
	"github.com/lxn/win"
)

func runGUIWithConfigAndTray(path string, startInTray bool) error {
	return runModernDashboard(path, startInTray)
}

func runModernDashboard(path string, startInTray bool) error {
	instance, acquired, err := acquireProcessMutex("Local\\SonDNS.GUI")
	if err != nil {
		return err
	}
	if !acquired {
		titlePtr, _ := syscall.UTF16PtrFromString("SonDNS")
		if hwnd := win.FindWindow(nil, titlePtr); hwnd != 0 {
			restoreMainWindow(hwnd)
		}
		return nil
	}
	defer instance.Close()
	c, err := LoadConfig(path)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wv := webview2.New(false)
	if wv == nil {
		return fmt.Errorf("WebView2 runtime is unavailable")
	}
	defer wv.Destroy()
	wv.SetTitle("SonDNS")
	wv.SetSize(1240, 820, webview2.HintNone)
	bindModernDashboard(wv, path, c)
	wv.SetHtml(modernDashboardHTML)
	if startInTray {
		hwnd := win.HWND(uintptr(wv.Window()))
		win.ShowWindow(hwnd, win.SW_HIDE)
	}
	wv.Run()
	return nil
}

func bindModernDashboard(wv webview2.WebView, path string, c Config) {
	_ = wv.Bind("getConnections", func() ([]ConnectionEntry, error) {
		resp, err := callDNSIPC(dnsIPCRequest{Op: "connections", Limit: 500})
		return resp.Connections, err
	})
	_ = wv.Bind("getDNSRequests", func() ([]DNSRequestEntry, error) {
		resp, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
		return resp.Entries, err
	})
	_ = wv.Bind("getStatus", func() (map[string]interface{}, error) {
		state, err := queryServiceSnapshot()
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"state": state.text(), "capture_mode": c.DPI.CaptureMode}, nil
	})
	_ = wv.Bind("blockApp", func(appPath string) error {
		_, err := callDNSIPC(dnsIPCRequest{Op: "block_app", Path: appPath})
		return err
	})
	_ = wv.Bind("unblockApp", func(appPath string) error {
		_, err := callDNSIPC(dnsIPCRequest{Op: "unblock_app", Path: appPath})
		return err
	})
	_ = wv.Bind("setProvider", func(provider string) error {
		upstream := PredefinedUpstream(provider)
		cfg := c
		cfg.Upstreams = []Upstream{upstream}
		if err := SaveConfig(path, cfg); err != nil {
			return err
		}
		_, err := callDNSIPC(dnsIPCRequest{Op: "set_upstream", Upstreams: cfg.Upstreams})
		return err
	})
	_ = wv.Bind("setCaptureMode", func(mode string) error {
		mode = strings.ToLower(strings.TrimSpace(mode))
		cfg := c
		cfg.DPI.CaptureMode = mode
		if err := cfg.normalize(); err != nil {
			return err
		}
		if err := SaveConfig(path, cfg); err != nil {
			return err
		}
		return restartService()
	})
	_ = wv.Bind("restartService", func() error { return restartService() })
	_ = wv.Bind("stopService", func() error { return stopService() })
	_ = wv.Bind("startService", func() error { return startInstalledService() })
	_ = wv.Bind("uninstallService", func() error { return uninstallService() })
}

var modernDashboardHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>SonDNS</title>
<style>
:root{color-scheme:dark;font:14px Segoe UI,system-ui,sans-serif;background:#07111f;color:#e2e8f0}*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at top right,#12345a,#07111f 55%);min-height:100vh}header{height:72px;padding:0 28px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #22344d;background:#0b1728dd;backdrop-filter:blur(18px)}.brand{display:flex;align-items:center;gap:12px;font-size:21px;font-weight:700}.logo{width:30px;height:30px;border-radius:9px;background:linear-gradient(135deg,#38bdf8,#2563eb);box-shadow:0 0 26px #38bdf855}.status{color:#8fb8df;font-size:12px}.layout{display:flex;min-height:calc(100vh - 72px)}nav{width:210px;padding:22px 14px;border-right:1px solid #22344d;background:#091525aa}nav button{display:block;width:100%;text-align:left;border:0;background:transparent;color:#91a7c1;border-radius:9px;padding:12px 14px;margin-bottom:6px;font-size:14px;cursor:pointer}nav button.active,nav button:hover{background:#1e3a5f;color:#e0f2fe}main{flex:1;padding:24px 28px;min-width:0}.page{display:none}.page.active{display:block}.cards{display:grid;grid-template-columns:repeat(4,minmax(140px,1fr));gap:14px;margin-bottom:20px}.card{border:1px solid #263d59;border-radius:14px;padding:16px;background:#0f2034cc;box-shadow:0 12px 32px #02061755}.metric{font-size:25px;font-weight:700;color:#e0f2fe}.label{font-size:12px;color:#8ba2bd;margin-top:5px}.panel{border:1px solid #263d59;border-radius:14px;background:#0d1b2dcc;overflow:hidden;margin-bottom:18px}.panel h2{font-size:15px;margin:0;padding:15px 18px;border-bottom:1px solid #263d59}.toolbar{padding:12px 16px;display:flex;gap:8px;align-items:center}.toolbar select,.toolbar input{background:#091525;color:#e2e8f0;border:1px solid #38516f;border-radius:7px;padding:8px}.btn{background:#1d4ed8;color:#eff6ff;border:0;border-radius:7px;padding:8px 12px;cursor:pointer}.btn.secondary{background:#24364f}.btn.danger{background:#b91c1c}table{width:100%;border-collapse:collapse;font-size:12px}th{text-align:left;color:#8ea7c4;background:#13263e;position:sticky;top:0}th,td{padding:10px 12px;border-bottom:1px solid #1e334d;white-space:nowrap;max-width:280px;overflow:hidden;text-overflow:ellipsis}tr:hover{background:#153252}.muted{color:#8ba2bd}.pill{padding:3px 7px;border-radius:999px;background:#064e3b;color:#a7f3d0}.empty{padding:32px;text-align:center;color:#8ba2bd}.form{display:grid;grid-template-columns:180px 240px;gap:14px;align-items:center}.form label{color:#9ab0c7}.form select{background:#091525;color:#e2e8f0;border:1px solid #38516f;border-radius:7px;padding:8px}
</style></head><body><header><div class="brand"><span class="logo"></span>SonDNS</div><div class="status" id="status">Starting service monitor…</div></header><div class="layout"><nav><button class="active" data-page="overview">Overview</button><button data-page="connections">Connections</button><button data-page="dns">DNS requests</button><button data-page="apps">Apps & firewall</button><button data-page="settings">Settings</button></nav><main>
<section id="overview" class="page active"><div class="cards"><div class="card"><div class="metric" id="metricConnections">0</div><div class="label">Active connections</div></div><div class="card"><div class="metric" id="metricApps">0</div><div class="label">Applications</div></div><div class="card"><div class="metric" id="metricBlocked">0</div><div class="label">Blocked apps</div></div><div class="card"><div class="metric" id="metricDNS">0</div><div class="label">DNS requests</div></div></div><div class="panel"><h2>Live network activity</h2><div class="toolbar"><button class="btn" onclick="show('connections')">Open connections</button><button class="btn secondary" onclick="refreshAll()">Refresh</button></div><div id="overviewRows" class="empty">Loading…</div></div></section>
<section id="connections" class="page"><div class="panel"><h2>Connections</h2><div class="toolbar"><input id="connectionFilter" placeholder="Filter process, domain, IP…" oninput="renderConnections()"><button class="btn secondary" onclick="refreshAll()">Refresh</button></div><div style="max-height:620px;overflow:auto"><table><thead><tr><th>Process</th><th>PID</th><th>Domain</th><th>Remote</th><th>Protocol</th><th>State</th><th>Firewall</th></tr></thead><tbody id="connectionRows"></tbody></table></div></div></section>
<section id="dns" class="page"><div class="panel"><h2>DNS requests</h2><div style="max-height:620px;overflow:auto"><table><thead><tr><th>Time</th><th>Domain</th><th>Type</th><th>Result</th><th>Latency</th></tr></thead><tbody id="dnsRows"></tbody></table></div></div></section>
<section id="apps" class="page"><div class="panel"><h2>Apps & firewall</h2><div class="toolbar"><span class="muted">Default policy: allow + monitor. Block is applied to outbound Windows Firewall rules.</span></div><div id="appRows" class="empty">Loading…</div></div></section>
<section id="settings" class="page"><div class="panel"><h2>Settings</h2><div class="toolbar"><div class="form"><label>DNS provider</label><select id="provider" onchange="setProvider(this.value)"><option>SonCMS</option><option>NextDNS</option></select><label>DPI capture mode</label><select id="capture" onchange="setCaptureMode(this.value)"><option value="default">Default</option><option value="tcp">TCP</option><option value="tcp+udp">TCP + QUIC UDP</option></select></div></div></div></section>
</main></div><script>
let connections=[],dns=[];const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function show(id){document.querySelectorAll('.page').forEach(x=>x.classList.toggle('active',x.id===id));document.querySelectorAll('nav button').forEach(x=>x.classList.toggle('active',x.dataset.page===id));}
document.querySelectorAll('nav button').forEach(x=>x.onclick=()=>show(x.dataset.page));
function renderConnections(){let f=(document.getElementById('connectionFilter')?.value||'').toLowerCase();let xs=connections.filter(x=>(x.process_path+' '+x.domain+' '+x.remote_ip).toLowerCase().includes(f));document.getElementById('connectionRows').innerHTML=xs.map(x=>'<tr><td title="'+esc(x.process_path)+'">'+esc((x.process_path||'').split('\\').pop()||'—')+'</td><td>'+x.pid+'</td><td>'+esc(x.domain||'—')+'</td><td>'+esc(x.remote_ip)+':'+x.remote_port+'</td><td><span class="pill">'+esc(x.protocol)+'</span></td><td>'+esc(x.state)+'</td><td><button class="btn '+(x.decision==='block'?'':'danger')+'" onclick="toggleApp(\''+esc(x.process_path)+'\',\''+esc(x.decision)+'\')">'+(x.decision==='block'?'Allow':'Block')+'</button></td></tr>').join('')||'<tr><td colspan="7" class="empty">No connections</td></tr>';}
function renderDNS(){document.getElementById('dnsRows').innerHTML=dns.slice(-250).reverse().map(x=>'<tr><td>'+esc(x.ts)+'</td><td>'+esc(x.domain)+'</td><td>'+esc(x.type)+'</td><td>'+esc(x.result)+'</td><td>'+x.duration_us/1000+' ms</td></tr>').join('')||'<tr><td colspan="5" class="empty">No DNS requests</td></tr>';}
function renderApps(){let map={};connections.forEach(x=>{let p=x.process_path||'Unknown';(map[p]??=[]).push(x)});document.getElementById('appRows').innerHTML=Object.keys(map).map(p=>'<div class="toolbar"><b>'+esc(p)+'</b><span class="muted">'+map[p].length+' connections</span><button class="btn danger" onclick="toggleApp(\''+esc(p)+'\',\'allow\')">Block</button></div>').join('')||'<div class="empty">No applications</div>';}
async function toggleApp(p,d){try{if(d==='block')await window.unblockApp(p);else await window.blockApp(p);await refreshAll()}catch(e){alert(e)}}
async function refreshAll(){try{connections=await window.getConnections();dns=await window.getDNSRequests();let st=await window.getStatus();document.getElementById('status').textContent=st.state+' · '+st.capture_mode;document.getElementById('metricConnections').textContent=connections.length;document.getElementById('metricApps').textContent=new Set(connections.map(x=>x.process_path)).size;document.getElementById('metricBlocked').textContent=connections.filter(x=>x.decision==='block').length;document.getElementById('metricDNS').textContent=dns.length;renderConnections();renderDNS();renderApps();document.getElementById('overviewRows').innerHTML=connections.slice(0,8).map(x=>'<div class="toolbar"><b>'+esc((x.process_path||'').split('\\').pop())+'</b><span>'+esc(x.domain||x.remote_ip)+':'+x.remote_port+'</span><span class="muted">'+esc(x.state)+'</span></div>').join('')||'<div class="empty">No active connections</div>'}catch(e){document.getElementById('status').textContent='Service unavailable'}}
async function setProvider(v){try{await window.setProvider(v);await refreshAll()}catch(e){alert(e)}}async function setCaptureMode(v){try{await window.setCaptureMode(v);await refreshAll()}catch(e){alert(e)}}refreshAll();setInterval(refreshAll,1000);
</script></body></html>`
