# SonDNS

SonDNS là tiện ích DNS-over-HTTPS cho Windows, dùng lõi AdGuard dnsproxy, GUI native Walk và service WinDivert trong một file `SonDNS.exe`.

## Giao diện

- Cửa sổ cố định 560×420, bảng DNS request luôn hiển thị tối thiểu 8 hàng; không có Expand/Collapse.
- Titlebar và tray dùng tên SonDNS; hàng tiêu đề lặp bên trong cửa sổ đã được bỏ.
- Bảng gồm `Time / Domain / Type / Result / Answer IP / Latency`. Kéo mép tiêu đề để đổi độ rộng cột.
- Chuột phải một request để copy domain hoặc toàn bộ IP A/AAAA trả về.
- `Clear log` xóa ring buffer trong RAM của service. `Clear DNS cache` gọi trực tiếp cache của dnsproxy.
- Result phân biệt RCODE DNS (`NOERROR`, `NXDOMAIN`, `REFUSED`, …) với lỗi vận chuyển (`ERROR`, `TIMEOUT`); chọn dòng lỗi để xem chi tiết trong tooltip.

DNS request log chỉ tồn tại trong RAM, tối đa 200 bản ghi gần nhất. Không tạo hoặc ghi `dns_log_file`, không lưu raw packet hay client IP. GUI trao đổi snapshot và lệnh clear với service qua named pipe local `SonDNS`.

## Build

Cần Go 1.27.1 trên Windows amd64:

```powershell
go test ./...
go vet ./...
.\scripts\build.ps1
```

Script tạo `SonDNS.exe` và nhúng manifest Common Controls 6, PerMonitorV2, asInvoker.

## Service và cấu hình

```powershell
.\SonDNS.exe init --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.exe install --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.exe stop
.\SonDNS.exe uninstall
```

GUI chỉ đọc cấu hình và trạng thái khi mở. Thay đổi DNS Server (SonCMS / NextDNS) được hot-swap tức thì qua IPC không cần restart. `Apply settings` lưu interception và autostart.
SonDNS dùng mutex riêng cho GUI và runtime: mở GUI lần hai sẽ tự kích hoạt cửa sổ hiện tại lên, còn một runtime thứ hai bị từ chối trước khi bind cổng DNS. Service Windows vốn đã được SCM giới hạn một instance.

Các upstream hỗ trợ `https://`, `tls://`, `quic://`, `sdns://`; cache mặc định 64 MiB.

## WinDivert

Chế độ System DNS cần Administrator. Driver `WinDivert.dll` và `WinDivert64.sys` đã được nhúng thẳng trong EXE, tự động giải phóng khi cần. WinDivert bắt IPv4/IPv6 UDP/53 ngoài loopback; listener cục bộ mặc định `127.0.0.1:5300`.
TCP/53, DoH/DoT trực tiếp của ứng dụng, mDNS/LLMNR và DNS nằm trong tunnel VPN không đi qua pipeline này nên không xuất hiện trong request log.

## Kiểm thử Windows

```powershell
$env:SONDNS_UI_TEST = '1'; go test -run TestNativeLayout -count=1 -v; Remove-Item Env:SONDNS_UI_TEST
$env:SONDNS_SERVICE_TEST = '1'; go test -run TestWindowsServiceLifecycle -count=1 -v; Remove-Item Env:SONDNS_SERVICE_TEST
```
