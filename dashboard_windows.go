//go:build windows

package main

import (
	"sync"

	webview2 "github.com/jchv/go-webview2"
)

var dashboardMu sync.Mutex
var dashboardOpen bool

func openConnectionDashboard() {
	dashboardMu.Lock()
	if dashboardOpen {
		dashboardMu.Unlock()
		return
	}
	dashboardOpen = true
	dashboardMu.Unlock()
	go func() {
		defer func() {
			dashboardMu.Lock()
			dashboardOpen = false
			dashboardMu.Unlock()
		}()
		wv := webview2.New(false)
		if wv == nil {
			return
		}
		defer wv.Destroy()
		wv.SetTitle("SonDNS Connections")
		wv.SetSize(1180, 760, webview2.HintNone)
		_ = wv.Bind("getConnections", func() ([]ConnectionEntry, error) {
			resp, err := callDNSIPC(dnsIPCRequest{Op: "connections", Limit: 250})
			if err != nil {
				return nil, err
			}
			return resp.Connections, nil
		})
		_ = wv.Bind("blockApp", func(path string) error {
			_, err := callDNSIPC(dnsIPCRequest{Op: "block_app", Path: path})
			return err
		})
		_ = wv.Bind("unblockApp", func(path string) error {
			_, err := callDNSIPC(dnsIPCRequest{Op: "unblock_app", Path: path})
			return err
		})
		wv.SetHtml(connectionDashboardHTML)
		wv.Run()
	}()
}

var connectionDashboardHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>SonDNS Connections</title>
<style>
:root{color-scheme:dark;font-family:Segoe UI,system-ui,sans-serif;background:#0f172a;color:#e2e8f0}
body{margin:0;background:linear-gradient(135deg,#0f172a,#172554);min-height:100vh}
header{padding:22px 28px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #334155;background:#111827cc;backdrop-filter:blur(16px)}
h1{margin:0;font-size:22px;letter-spacing:.2px}.status{color:#93c5fd;font-size:13px}
main{padding:22px 28px}.card{border:1px solid #334155;border-radius:14px;background:#111827dd;box-shadow:0 12px 40px #02061766;overflow:hidden}
table{width:100%;border-collapse:collapse;font-size:13px}th{text-align:left;color:#94a3b8;font-weight:600;background:#1e293bcc;position:sticky;top:0}th,td{padding:11px 12px;border-bottom:1px solid #1e293b}tr:hover{background:#1e3a5f66}
.muted{color:#94a3b8}.pill{padding:3px 7px;border-radius:999px;background:#064e3b;color:#a7f3d0;font-size:11px}.btn{border:1px solid #475569;background:#1e293b;color:#e2e8f0;border-radius:7px;padding:5px 9px;cursor:pointer}.btn:hover{background:#334155}
</style></head><body><header><h1>SonDNS Connections</h1><div class="status" id="status">Loading…</div></header>
<main><div class="card"><table><thead><tr><th>Process</th><th>Domain</th><th>Remote</th><th>Protocol</th><th>State</th><th>Last seen</th><th>Firewall</th></tr></thead><tbody id="rows"></tbody></table></div></main>
<script>
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function refresh(){try{const xs=await window.getConnections();document.getElementById('status').textContent=xs.length+' connections';document.getElementById('rows').innerHTML=xs.map(function(x){return '<tr><td title="'+esc(x.process_path)+'">'+esc((x.process_path||'').split('\\\\').pop()||'PID '+x.pid)+'<br><span class="muted">PID '+x.pid+'</span></td><td>'+esc(x.domain||'—')+'</td><td>'+esc(x.remote_ip)+':'+x.remote_port+'</td><td><span class="pill">'+esc(x.protocol)+'</span></td><td>'+esc(x.state)+'</td><td>'+new Date(x.last_seen).toLocaleTimeString()+'</td><td><button class="btn" onclick="toggle(\''+esc(x.process_path)+'\',\''+esc(x.decision)+'\')">'+(x.decision==='block'?'Allow':'Block app')+'</button></td></tr>';}).join('')}catch(e){document.getElementById('status').textContent='Service unavailable';}}
async function toggle(path,decision){if(!path)return;try{if(decision==='block')await window.unblockApp(path);else await window.blockApp(path);await refresh()}catch(e){alert(e)}}
refresh();setInterval(refresh,1000);
</script></body></html>`
