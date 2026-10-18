# SonDNS

SonDNS là tiện ích DNS-over-HTTPS portable cho Windows, dùng lõi AdGuard dnsproxy, GUI native Walk và service WinDivert trong một file `SonDNS.exe`.

Bản chính dùng GUI/tray. EXE nhúng DNS core, WinDivert và DPI runtime; mọi file ứng dụng nằm cạnh EXE, trong `data/` và `runtime/`. Không ghi vào AppData, ProgramData hay Program Files. Xem [hướng dẫn portable](../PORTABLE.md) về cách chạy, di chuyển và quyền Administrator.

Menu service có **Migrate existing service…** để chuyển từ bản CMD/folder cũ sau
khi xem trước và xác nhận. Bản mới backup EXE/config/runtime và trạng thái
SCM/tray vào `backups/` cạnh EXE. Nếu rollback gặp folder cũ không an toàn cho
SYSTEM, DNS có thể để dừng; xem báo cáo recovery. Chưa kiểm thử migration/driver
đầy đủ trên máy Windows sạch.

## Giao diện

- Cửa sổ cố định 720×420, bảng DNS request luôn hiển thị tối thiểu 8 hàng; không có Expand/Collapse.
- Titlebar và tray dùng tên SonDNS; hàng tiêu đề lặp bên trong cửa sổ đã được bỏ.
- Bảng gồm Time / Domain / Type / Result / Latency; không hiển thị cột Upstream. Kéo mép tiêu đề để đổi độ rộng cột.
- Chuột phải một request để copy domain hoặc toàn bộ IP A/AAAA trả về.
- `Clear log` xóa ring buffer trong RAM của service.
- Result phân biệt RCODE DNS (`NOERROR`, `NXDOMAIN`, `REFUSED`, …) với lỗi vận chuyển (`ERROR`, `TIMEOUT`); chọn dòng lỗi để xem chi tiết trong tooltip.

DNS request log chỉ tồn tại trong RAM, tối đa 200 bản ghi gần nhất. Không tạo hoặc ghi `dns_log_file`, không lưu raw packet hay client IP. GUI trao đổi snapshot và lệnh clear với service qua named pipe local `SonDNS`.

## Build

Cần Go 1.27.2 trên Windows amd64:

Đóng gói source kèm dependencies, build offline và các điều kiện trước khi
phát hành được mô tả trong [RELEASING.md](../RELEASING.md).

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

Bản phát hành có chữ ký dùng `.\scripts\build.ps1 -Release`; lệnh này yêu cầu certificate và `signtool.exe`, rồi tạo EXE, `SHA256SUMS` và `build-info.json` trong `dist\SonDNS-Portable`. Build thường là preview unsigned; cần hoàn tất license/nguồn tương ứng trước khi phát hành công khai.

`signtool.exe` (Windows SDK) và certificate phải có sẵn trong `Cert:\CurrentUser\My`. Không nên dùng UPX/packer, obfuscator hoặc hướng dẫn người dùng tắt Defender; các cách đó thường làm tăng cảnh báo và giảm độ tin cậy. Nếu Microsoft Defender vẫn nhận nhầm, gửi đúng file đã ký cùng mã phát hiện tại [Microsoft Security Intelligence file submission](https://www.microsoft.com/en-us/wdsi/filesubmission).

## Service và cấu hình

```powershell
.\SonDNS.exe init
.\SonDNS.exe install
.\SonDNS.exe stop
.\SonDNS.exe uninstall
```

`uninstall` gỡ service/driver thuộc bản EXE này và giữ nguyên toàn bộ thư mục
portable, gồm cấu hình, log, runtime và backup. Không xóa dữ liệu của bản cũ
trong ProgramData. Gỡ service trước khi chuyển thư mục, rồi cài lại ở chỗ mới.

GUI chỉ đọc trạng thái khi mở, không tự khởi động service. Nếu chưa cài hoặc service đang dừng, dùng nút Install & start / Start với quyền Administrator. Menu DNS Server dùng cùng dropdown với DPI, gồm SonCMS và NextDNS; thay đổi lựa chọn được hot-swap qua IPC không cần restart. SonCMS dùng endpoint https://dns.soncms.net/dns-query/sondns. Menu DPI có Disabled, Default, TCP và TCP + QUIC UDP; Disabled tắt riêng zapret2 nhưng vẫn giữ WinDivert DNS interception. Start with Windows điều khiển cả service startup và GUI tray entry; Registry Run chỉ tồn tại khi tùy chọn này được bật.
SonDNS dùng mutex riêng cho GUI và runtime: mở GUI lần hai sẽ tự kích hoạt cửa sổ hiện tại lên, còn một runtime thứ hai bị từ chối trước khi bind cổng DNS. Service Windows vốn đã được SCM giới hạn một instance.

Nút **Manage…** mở phiên quản trị qua UAC; hủy UAC giữ nguyên cửa sổ hiện tại. Service kiểm tra owner/quyền ghi của toàn bộ folder ứng dụng và các folder cha. Khi cần, menu **Create portable copy…** tạo bản mới ở vị trí được chọn với quyền ghi chỉ dành cho Administrator/SYSTEM, giữ nguyên source và service đang chạy. Xem [PORTABLE.md](../PORTABLE.md).

Các upstream hỗ trợ `https://`, `tls://`, `quic://`, `sdns://`. SonDNS không cache DNS local: mọi request đều được gửi tới upstream, kể cả query trùng nhau. Các trường cache trong cấu hình cũ được bỏ qua.

## Bypass DNS qua WinDivert

`interception: "windivert"` là chế độ duy nhất và mặc định để bypass DNS của nhà mạng: WinDivert bắt IPv4/IPv6 UDP/53 ngoài loopback rồi chuyển query qua upstream mã hóa. Chế độ này cần Administrator và vẫn hoạt động khi DPI bypass tắt. Driver `WinDivert.dll` và `WinDivert64.sys` được nhúng trong EXE, kiểm tra SHA-256 và giải nén vào `runtime/embedded/` cạnh EXE. Dừng service gỡ đăng ký driver thuộc bản này, giữ các file portable. SonDNS không hỗ trợ tắt interception trong cấu hình hoặc giao diện.

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

Thông báo giấy phép và nguồn của các thành phần tích hợp nằm trong [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

## Kiểm thử Windows

```powershell
$env:SONDNS_UI_TEST = '1'; go test -run TestNativeLayout -count=1 -v ./internal/gui; Remove-Item Env:SONDNS_UI_TEST
```

Kiểm thử lifecycle cài service/driver thật và tạo hai tài khoản Windows tạm.
Chỉ chạy trên VM sạch dùng riêng cho test, theo [bộ chạy kiểm thử Windows](CLEAN_WINDOWS_TESTS.md)
hoặc workflow disposable trên GitHub Actions. Bộ chạy kiểm tra điều kiện và bật
riêng các opt-in service/account; không chạy lifecycle trên máy đang dùng SonDNS.
