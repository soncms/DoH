# SonDNS

SonDNS là tiện ích DNS-over-HTTPS cho Windows, dùng lõi AdGuard dnsproxy, GUI native Walk và service WinDivert trong một file `SonDNS.exe`.

## Giao diện

- Cửa sổ cố định 720×420, bảng DNS request luôn hiển thị tối thiểu 8 hàng; không có Expand/Collapse.
- Titlebar và tray dùng tên SonDNS; hàng tiêu đề lặp bên trong cửa sổ đã được bỏ.
- Bảng gồm Time / Domain / Type / Result / Latency; không hiển thị cột Upstream. Kéo mép tiêu đề để đổi độ rộng cột.
- Chuột phải một request để copy domain hoặc toàn bộ IP A/AAAA trả về.
- `Clear log` xóa ring buffer trong RAM của service.
- Result phân biệt RCODE DNS (`NOERROR`, `NXDOMAIN`, `REFUSED`, …) với lỗi vận chuyển (`ERROR`, `TIMEOUT`); chọn dòng lỗi để xem chi tiết trong tooltip.

DNS request log chỉ tồn tại trong RAM, tối đa 200 bản ghi gần nhất. Không tạo hoặc ghi `dns_log_file`, không lưu raw packet hay client IP. GUI trao đổi snapshot và lệnh clear với service qua named pipe local `SonDNS`.

## Build

Cần Go 1.27.1 trên Windows amd64:

```powershell
go test ./...
go vet ./...
.\scripts\verify-assets.ps1
.\scripts\build.ps1
```

Script tạo `SonDNS.exe` và nhúng manifest Common Controls 6, PerMonitorV2, asInvoker. Bản build thường giữ metadata của Go thay vì strip symbol để hỗ trợ phân tích và giảm tín hiệu heuristic không cần thiết.

Khi phát hành, nên ký Authenticode bằng chứng thư của nhà phát hành và dùng timestamp server:

```powershell
$env:SONDNS_SIGN_CERT_THUMBPRINT = "THUMBPRINT_CERTIFICATE"
$env:SONDNS_TIMESTAMP_URL = "http://timestamp.digicert.com"
.\scripts\build.ps1 -Sign
```

Bản phát hành chính thức dùng `.\scripts\build.ps1 -Release`; lệnh này yêu cầu certificate và `signtool.exe`, rồi tạo `dist\SHA256SUMS` và `dist\build-info.json`. Build thường vẫn unsigned để phát triển, nhưng không được phát hành.

`signtool.exe` (Windows SDK) và certificate phải có sẵn trong `Cert:\CurrentUser\My`. Không nên dùng UPX/packer, obfuscator hoặc hướng dẫn người dùng tắt Defender; các cách đó thường làm tăng cảnh báo và giảm độ tin cậy. Nếu Microsoft Defender vẫn nhận nhầm, gửi đúng file đã ký cùng mã phát hiện tại [Microsoft Security Intelligence file submission](https://www.microsoft.com/en-us/wdsi/filesubmission).

## Service và cấu hình

```powershell
.\SonDNS.exe init --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.exe install --config C:\ProgramData\SonDNS\sondns.json
.\SonDNS.exe stop
.\SonDNS.exe uninstall
```

`uninstall` stops and removes the SonDNS/WinDivert services, removes the
runtime/config/log tree under `%ProgramData%\SonDNS`, and deletes SonDNS backup
executables next to the installed binary. The main `SonDNS.exe` and GUI stay in
place so the service can be installed again immediately.

GUI chỉ đọc trạng thái khi mở, không tự khởi động service. Nếu chưa cài hoặc service đang dừng, dùng nút Install & start / Start với quyền Administrator. Menu DNS Server dùng cùng dropdown với DPI, gồm SonCMS và NextDNS; thay đổi lựa chọn được hot-swap qua IPC không cần restart. SonCMS dùng endpoint https://dns.soncms.net/dns-query/sondns. Menu DPI có Disabled, Default, TCP và TCP + QUIC UDP; Disabled tắt riêng zapret2 nhưng vẫn giữ WinDivert DNS interception. Start with Windows điều khiển cả service startup và GUI tray entry; Registry Run chỉ tồn tại khi tùy chọn này được bật.
SonDNS dùng mutex riêng cho GUI và runtime: mở GUI lần hai sẽ tự kích hoạt cửa sổ hiện tại lên, còn một runtime thứ hai bị từ chối trước khi bind cổng DNS. Service Windows vốn đã được SCM giới hạn một instance.

Các upstream hỗ trợ `https://`, `tls://`, `quic://`, `sdns://`. SonDNS không cache DNS local: mọi request đều được gửi tới upstream, kể cả query trùng nhau. Các trường cache trong cấu hình cũ được bỏ qua.

## Bypass DNS qua WinDivert

`interception: "windivert"` là chế độ duy nhất và mặc định để bypass DNS của nhà mạng: WinDivert bắt IPv4/IPv6 UDP/53 ngoài loopback rồi chuyển query qua upstream mã hóa. Chế độ này cần Administrator và vẫn hoạt động khi DPI bypass tắt. Driver `WinDivert.dll` và `WinDivert64.sys` đã được nhúng thẳng trong EXE, kiểm tra SHA-256 trước khi giải phóng và được dọn khi dừng. SonDNS không hỗ trợ tắt interception trong cấu hình hoặc giao diện.

SonDNS không xử lý TCP/53, DoH/DoT trực tiếp của ứng dụng, mDNS/LLMNR hoặc DNS nằm trong tunnel VPN trong request log. Lưu lượng HTTP(S)/QUIC được xử lý bởi backend zapret2 tích hợp riêng.

## Bypass DPI tích hợp sẵn

SonDNS tích hợp `winws2` của zapret2 cùng LuaJIT runtime, Lua strategy, filter và WinDivert. Các asset được nhúng vào `SonDNS.exe`, kiểm tra theo manifest SHA-256 và chỉ giải nén/chạy khi `dpi_bypass.enabled` là `true`. Máy đích không cần cài thêm helper hay copy thêm DLL.

Trong `sondns.json`:

```json
{
  "dpi_bypass": {
    "enabled": false,
    "backend": "embedded-zapret2",
    "executable": "",
    "arguments": [],
    "targets": [],
    "exclude": ["localhost", "*.local"],
    "fail_open": true,
    "auto_restart": true
  }
}
```

Backend được khởi động trong service, ghi stdout/stderr vào log SonDNS, tự khởi động lại tối đa bốn lần khi thoát bất thường và bị dừng sạch khi service dừng. `targets` rỗng nghĩa là áp dụng profile mặc định cho HTTP/HTTPS/QUIC; nếu chỉ muốn giới hạn domain, điền từng host vào danh sách. Có thể dùng `arguments` để thay profile mặc định, nhưng khi làm vậy cần giữ các đường dẫn Lua/filter hợp lệ. Engine chạy fail-open: nếu không thể khởi động, DNS SonDNS vẫn tiếp tục hoạt động.

`capture_mode` có ba giá trị: `default`, `tcp`, và `tcp+udp`. Chế độ `tcp+udp` bắt toàn bộ TCP (trừ TCP/53) và UDP/443 cho QUIC, đồng thời giữ raw filter cho các protocol UDP đã nhận diện; loopback và địa chỉ LAN được WinDivert loại trừ.

Thông báo giấy phép và nguồn của các thành phần tích hợp nằm trong [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Kiểm thử Windows

```powershell
$env:SONDNS_UI_TEST = '1'; go test -run TestNativeLayout -count=1 -v; Remove-Item Env:SONDNS_UI_TEST
$env:SONDNS_SERVICE_TEST = '1'; go test -run TestWindowsServiceLifecycle -count=1 -v; Remove-Item Env:SONDNS_SERVICE_TEST
```
