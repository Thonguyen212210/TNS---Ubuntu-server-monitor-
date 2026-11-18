#!/bin/sh
# TNS installer — chạy với quyền root trên server Linux:
#   sudo sh install.sh            (tự chọn binary tns-linux-<arch> nằm cùng thư mục)
#   sudo sh install.sh ./tns    (chỉ định binary)
set -eu

[ "$(id -u)" -eq 0 ] || { echo "Cần chạy bằng root (sudo)."; exit 1; }

DIR=$(cd "$(dirname "$0")" && pwd)
BIN=${1:-}
if [ -z "$BIN" ]; then
  case "$(uname -m)" in
    x86_64|amd64) BIN="$DIR/tns-linux-amd64" ;;
    aarch64|arm64) BIN="$DIR/tns-linux-arm64" ;;
    *) echo "Kiến trúc $(uname -m) chưa có bản build sẵn — hãy build từ mã nguồn."; exit 1 ;;
  esac
fi
[ -f "$BIN" ] || { echo "Không tìm thấy binary: $BIN"; exit 1; }

install -m 0755 "$BIN" /usr/local/bin/tns
mkdir -p /var/lib/tns && chmod 700 /var/lib/tns

cat > /etc/systemd/system/tns.service <<'EOF'
[Unit]
Description=TNS server panel
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/tns -addr :1611 -data /var/lib/tns
Restart=on-failure
RestartSec=3
User=root
Environment=HOME=/root
Environment=SHELL=/bin/bash
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now tns
sleep 1
systemctl --no-pager --lines=3 status tns || true

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "✔ TNS đã chạy:  http://${IP:-<server-ip>}:1611"
echo "  Lần đầu mở trang sẽ yêu cầu đặt mật khẩu quản trị."
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
  echo "  ufw đang bật — mở cổng nếu cần:  sudo ufw allow 1611/tcp"
fi
echo "  Khuyến nghị: chạy với HTTPS — sửa ExecStart thêm cờ -tls, rồi: systemctl restart tns"
