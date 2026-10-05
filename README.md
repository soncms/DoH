# SonDNS

SonDNS là ứng dụng DNS-over-HTTPS cho Windows. Go giữ toàn bộ core: DNS proxy, WinDivert, zapret2, Windows service và named-pipe IPC. Electron/Vue cung cấp UI theo visual language của IVPN Desktop tại commit `07e24591a999c6e9974ae19b795d22465fdcf89d`; UI đã đổi branding và binding dữ liệu sang SonDNS.

## Kiến trúc và giao diện

- `SonDNS.exe`: Electron UI, không tự chạy service khi mở; `--tray` dùng cho Registry Run.
- `SonDNS.Service.exe`: headless Go core/service, service name Windows vẫn là `SonDNS`.
- `\\.\pipe\SonDNS\control`: newline-delimited JSON, schema v3, chỉ nhận local clients.
- `%ProgramData%\SonDNS\sondns.json`: nguồn cấu hình duy nhất của core.
- `%LOCALAPPDATA%\SonDNS\ui.json`: bounds/theme/language/route/tray preference của UI.

UI giữ titlebar, typography, spacing, card, switch, transition, dark/light token và tray behavior theo IVPN. Các route SonDNS là Home, DNS, DPI mode, Logs, Settings và Diagnostics. Không có account, VPN protocol, kill-switch, Proxy hay Fake SNI.

DPI mode hiển thị rõ `Disabled`, `Default`, `TCP`, `TCP + QUIC UDP`; `Disabled` chỉ tắt helper winws2/zapret2, WinDivert DNS interception vẫn hoạt động. Đổi DPI ghi config, restart service, kiểm tra runtime và rollback nếu restart lỗi. Provider SonCMS/NextDNS hot-swap qua IPC.

## Core CLI

```powershell
.\SonDNS.Service.exe init --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.Service.exe install --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.Service.exe start
.\SonDNS.Service.exe stop
.\SonDNS.Service.exe restart
.\SonDNS.Service.exe status --json
.\SonDNS.Service.exe autostart --enable
.\SonDNS.Service.exe autostart --disable
.\SonDNS.Service.exe uninstall
```

UI không cần quyền Administrator. Các thao tác cài/gỡ/start/stop/restart/autostart gọi core CLI; core tự yêu cầu UAC bằng ShellExecuteW `runas`. `autostart` cập nhật SCM startup type, Registry Run (`SonDNS.exe --tray`) và config theo transaction, rollback khi có bước lỗi.

Khi `install` gặp service SonDNS của bản cũ, core dừng service, giữ nguyên đường dẫn
config/log, tạo bản sao `sondns.json.backup-YYYYMMDD-HHMMSS`, đổi binary path sang
`SonDNS.Service.exe`, cập nhật tray entry rồi start lại. Nếu cập nhật hoặc start thất bại,
SCM và Registry Run được khôi phục về trạng thái trước migration.

## Build

Yêu cầu Go 1.27.1, Node/npm và Windows SDK trên Windows amd64. Chỉ dùng entrypoint `scripts/build.ps1`:

```powershell
.\scripts\build.ps1                 # dev, unsigned
$env:SONDNS_SIGN_CERT_THUMBPRINT = "THUMBPRINT_CERTIFICATE"
$env:SONDNS_TIMESTAMP_URL = "http://timestamp.digicert.com"
.\scripts\build.ps1 -Release       # release, bắt buộc ký
```

Build thực hiện asset manifest check, Go test/vet, `npm ci`, frontend unit/lint/build, Electron packaging, rồi tạo:

- `dist\SonDNS.exe`
- `dist\SonDNS.Service.exe`
- `dist\SonDNS-setup.exe`
- `dist\SonDNS-portable.zip`
- `dist\SHA256SUMS`
- `dist\build-info.json`

Release gate dừng trước khi tạo artifact nếu thiếu `signtool.exe`, certificate thumbprint hoặc timestamp URL. Ba PE do SonDNS phát hành (UI, service, installer) được ký SHA-256 + RFC3161 và chạy `signtool verify /pa /all`; ZIP chỉ được hash vì ZIP không có Authenticode. Không dùng UPX, packer, obfuscator hoặc strip symbol.

`dist\SonDNS-portable.zip` chứa UI, service, resources, config example, license, notices và runtime manifest. Không phát hành bản dev unsigned.

## IPC v3

Các operation ổn định gồm `snapshot`, `clear_log`, `get_state`, `get_config`, `set_upstream`, `set_dpi` và `diagnostics`. `snapshot` giữ tối đa 200 DNS request trong RAM, có version, instance id và overwritten count. Lỗi trả `code` ổn định như `UNKNOWN_OPERATION`, `VALIDATION_FAILED`, `CONFIG_SAVE_FAILED` hoặc `NOT_AVAILABLE`.

## Runtime và asset

WinDivert bắt outbound IPv4/IPv6 UDP/53 ngoài loopback rồi chuyển query qua upstream mã hóa. Runtime zapret2/winws2 và Lua/filter được nhúng; manifest `assets/runtime-sha256.json` kiểm tra trước khi giải nén, trước khi ghi vào `%ProgramData%` và sau khi ghi. Sai hash thì helper không chạy, DPI fail-open, DNS vẫn tiếp tục và log nêu expected/actual hash. Khi hợp lệ log ghi `asset verified` cùng SHA-256.

## Giấy phép và nguồn

SonDNS phát hành theo GPLv3 vì port shell/component/style từ IVPN Desktop. Xem [LICENSE](LICENSE), [NOTICE](NOTICE) và [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Nguồn UI IVPN được pin tại commit `07e24591a999c6e9974ae19b795d22465fdcf89d`.

## Kiểm thử

```powershell
go test ./...
go vet ./...
Push-Location ui
npm run test:unit
npm run lint
npm run build
Pop-Location
```

Windows E2E cần máy sạch và Defender bật để kiểm tra installer/portable, UAC, service lifecycle, WinDivert cleanup, DPI Disabled không extract winws2, Start with Windows và `Get-AuthenticodeSignature`.
