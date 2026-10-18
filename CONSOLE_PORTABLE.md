# SonDNS CMD portable

Mở `Start.cmd` hoặc `SonDNS.exe` để vào menu CMD. Chạy các thao tác thay đổi
service/cấu hình bằng quyền Administrator; chương trình tự mở cửa sổ UAC khi cần.

| Mục | Chức năng |
| --- | --- |
| 1 | Xem log DNS trực tiếp, Ctrl+C trở về menu |
| 2 | Đổi SonCMS / NextDNS / upstream tùy chỉnh |
| 3 | Kiểm tra và cập nhật dependency từ upstream |
| 4 | Cài đặt: trạng thái, DPI, DNSSEC, autostart, restart và rollback |
| 0 | Đóng menu; service vẫn chạy |

Bản CMD không mở Walk GUI/tray. Thư mục `runtime/` chứa EXE/DLL/Lua/filter
nguyên bản, `data/` chứa cấu hình và lock hash, `backups/` chứa các bản sao
trước khi cập nhật hoặc đổi service. Không cần Go hoặc Git trên máy chạy.

Bản đã triển khai chạy DNS/DPI qua một service duy nhất trong thư mục này.
Đóng cửa sổ CMD chỉ đóng bảng điều khiển; service vẫn chạy nền.
Các lệnh thử riêng dưới đây dành cho kiểm tra kỹ thuật:

```bat
SonDNS.exe logs --preview
SonDNS.exe preview --duration 30s
```

Khi cài sang máy hoặc vị trí mới: chạy `SonDNS.exe service install`.
Service SonDNS chạy từ thư mục này và đọc `data/sondns.json`.
Chương trình kiểm tra DNS/runtime,
lưu SCM/config/lock trước khi chuyển; khôi phục SCM nếu khởi động thất bại.
GUI autostart cũ được bỏ; đóng cửa sổ/tray GUI cũ nếu nó đang mở.
Thư mục được đặt quyền SYSTEM/Administrators ghi, Users đọc để bảo vệ service.
SCM lưu đường dẫn tuyệt đối: nếu di chuyển cả thư mục, cần bind lại ở vị trí mới.
`service remove` chỉ gỡ đăng ký service, giữ các file và backup.

```bat
SonDNS.exe service bind
SonDNS.exe service restart
SonDNS.exe service auto on
SonDNS.exe dns use SonCMS
SonDNS.exe dns dnssec on
SonDNS.exe dpi tcp+udp
SonDNS.exe logs --follow
```

## Cập nhật dependency

```bat
SonDNS.exe components check
SonDNS.exe components update
SonDNS.exe components rollback
SonDNS.exe components verify
```

Chọn **menu 3** để cập nhật. `update` tải bản stable mới nhất của
dnsproxy/WinDivert và commit mới nhất của bundle Windows chính chủ zapret2,
kiểm tra digest/hash/signature driver, chạy kiểm tra dnsproxy và winws2 trước
khi kích hoạt. Nếu service thuộc thư mục này đang chạy, chỉ dừng service đó
khi đã chuẩn bị xong. Nếu khởi động hoặc probe DNS thất bại, tự khôi phục lock
trước đó. Runtime cũ được giữ để rollback; cập nhật gián đoạn trước khi kích
hoạt không đổi lock đang dùng. Nếu hash lỗi dù phiên bản không đổi, `update`
sẽ tải lại bản sạch. Lần cài đầu chưa có phiên bản trước để rollback.

Nguồn gốc được ghi trong `data/components.lock.json`, gồm URL, version/commit,
SHA-256 archive và từng file. Không sửa EXE/DLL/Lua của các upstream này:

- dnsproxy: https://github.com/AdguardTeam/dnsproxy (Apache-2.0)
- WinDivert: https://github.com/basil00/WinDivert (LGPL-3.0/GPL-2.0)
- winws2/Lua/filter: https://github.com/bol-van/zapret-win-bundle và
  https://github.com/bol-van/zapret2 (MIT)
- Cygwin: https://cygwin.com/licensing.html (DLL: LGPL-3.0-or-later với ngoại lệ liên kết)

LICENSE của dnsproxy và WinDivert nằm cạnh runtime tương ứng. Source/bản phát
hành gốc ở các URL trên. Bản thân SonDNS là bộ điều khiển riêng; cập nhật
`SonDNS.exe` cần build bản mới, không nằm trong `components update`.

## Hành vi DNS

SonDNS giữ listener/WinDivert và log; dnsproxy.exe chạy nền trên loopback
ngẫu nhiên. Cache tắt, upstream song song, tối đa hai lần gửi từ SonDNS.
Đổi upstream khởi động helper mới, probe, chờ query cũ xong rồi chuyển.
DNSSEC là yêu cầu bản ghi DNSSEC và bảo toàn DO/AD của client; không bổ sung
bộ xác thực chữ ký DNSSEC riêng. Một upstream bật DNSSEC thì policy chung bật.
Hủy context dừng việc SonDNS chờ socket; helper kết thúc query theo timeout
riêng tối đa 2 giây. Retry nội bộ của upstream không thuộc bộ đếm hai lần này.

Build từ source bằng PowerShell 7:

```powershell
./scripts/package-console.ps1 -ConfigPath C:/ProgramData/SonDNS/sondns.json
```

Build unsigned Windows amd64, metadata/hash trong `build-info.json`/`SHA256SUMS`.
Test vận chuyển với EXE chính chủ cần đặt `SONDNS_DNSPROXY_TEST_EXE` tới EXE đó.
