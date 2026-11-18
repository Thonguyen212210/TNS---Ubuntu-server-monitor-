# TNS — bảng điều khiển server siêu nhẹ

Một file binary duy nhất (~9 MB, RAM ~15–25 MB), không cần cài thêm gì. Giao diện web chạy ở cổng **1611**.

| Tính năng | Chi tiết |
|---|---|
| **Tổng quan** | CPU / RAM / disk / tải hệ thống, biểu đồ 10 phút & 24 giờ, mạng, disk I/O, nhân CPU, top tiến trình (có kill) |
| **Đăng nhập toàn cầu** | Đọc `auth.log` / `secure` / journald → bản đồ thế giới, top quốc gia, biểu đồ 7 ngày, cảnh báo bất thường (đăng nhập thành công sau brute-force, quốc gia mới, root dùng mật khẩu, IP dò mật khẩu) + nút **Chặn IP** |
| **Tường lửa** | ufw (đầy đủ), firewalld, iptables. Khi bật tường lửa sẽ tự mở cổng SSH + 1611 để không bị khoá ngoài |
| **Ứng dụng** | Docker (start/stop/restart/pause/xoá, log, mở shell vào container, images), website Nginx (tạo mới từ mẫu proxy/tĩnh, sửa cấu hình có `nginx -t` + tự rollback, bật/tắt), dịch vụ systemd, cổng đang mở |
| **Tệp tin** | Duyệt, xem ảnh/video/audio/PDF/code, sửa & lưu, upload kéo-thả, tải thư mục dạng zip, đổi tên, xoá |
| **Terminal** | xterm.js + PTY thật, nhiều tab, giữ phiên khi chuyển trang, phóng to/thu nhỏ chữ, toàn màn hình |

## Cài lên server Linux

```bash
# trên máy dev
make linux                      # tạo dist/tns-linux-amd64, dist/tns-linux-arm64, dist/install.sh
scp -r dist root@SERVER:/tmp/tns

# trên server
sudo sh /tmp/tns/install.sh   # cài vào /usr/local/bin + systemd service "tns"
```

Mở `http://SERVER:1611` → lần đầu sẽ yêu cầu **đặt mật khẩu quản trị**.

## Tuỳ chọn dòng lệnh

| Cờ | Ý nghĩa |
|---|---|
| `-addr :1611` | địa chỉ lắng nghe |
| `-data DIR` | thư mục dữ liệu (mặc định `/var/lib/tns` khi chạy root, `~/.tns` khi không) |
| `-tls` | HTTPS với chứng chỉ tự ký (tự tạo) |
| `-nogeo` | tắt định vị IP qua ip-api.com |
| `-demo` | dữ liệu minh hoạ cho phần nào máy không có (dùng để xem thử trên macOS) |
| `-reset-password` | xoá mật khẩu, đặt lại ở lần mở trang tiếp theo |

## Bảo mật

- Mật khẩu lưu dạng bcrypt; khoá IP tăng dần sau 5 lần sai; cookie `HttpOnly` + `SameSite=Strict`; mọi request ghi đều cần header `X-TNS` (chống CSRF).
- Lệnh hệ thống luôn gọi trực tiếp (không qua shell) và kiểm tra đầu vào bằng regex.
- File HTML/SVG xem trong trình duyệt tệp được phục vụ ở chế độ sandbox để không chạy script.
- TNS chạy bằng root → nên bật `-tls` (hoặc đặt sau Nginx + Let's Encrypt) và giới hạn IP được vào cổng 1611 trong trang Tường lửa.
- Định vị IP gửi danh sách IP đăng nhập tới ip-api.com (có cache). Dùng `-nogeo` nếu không muốn.

## Phát triển

```bash
make demo     # build và chạy ở chế độ demo tại http://localhost:1611
make test     # unit test cho bộ phân tích log SSH / ufw / kiểm tra đầu vào
```

Cấu trúc: backend Go (`*.go`), frontend JS thuần không build-step trong `web/` (nhúng vào binary bằng `go:embed`). Biểu đồ và bản đồ vẽ bằng canvas tự viết; chỉ dùng thư viện ngoài là xterm.js (tải lười khi mở Terminal).
