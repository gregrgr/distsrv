#!/usr/bin/env bash
# distsrv — 一键部署脚本
# 目标：Ubuntu 22.04 / 24.04（也兼容 Debian 12）
# 用法：sudo ./deploy.sh [选项]
# 详见：./deploy.sh --help

set -euo pipefail

# ============================================================
# 默认值
# ============================================================
DOMAIN=""
EMAIL=""
ADMIN_USER="admin"
ADMIN_PASS=""
BINARY=""
BUILD=0
DEV_MODE=0
SKIP_UFW=0
NO_SYSTEMD=0
ASSUME_YES=0
ORG_NAME=""
ORG_SLUG=""
SUPPORT=""
SERVICE_USER="distsrv"
INSTALL_PREFIX="/usr/local/bin"
ETC_DIR="/etc/distsrv"
DATA_DIR="/var/lib/distsrv"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================
# Helpers
# ============================================================
c_red()   { printf '\033[31m%s\033[0m\n' "$*"; }
c_green() { printf '\033[32m%s\033[0m\n' "$*"; }
c_yellow(){ printf '\033[33m%s\033[0m\n' "$*"; }
c_bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
step()    { printf '\n\033[36m==>\033[0m \033[1m%s\033[0m\n' "$*"; }
fail()    { c_red "错误：$*"; exit 1; }
warn()    { c_yellow "警告：$*"; }

confirm() {
  local prompt="$1"
  if [[ "$ASSUME_YES" -eq 1 ]]; then return 0; fi
  local ans
  read -rp "$prompt [y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]]
}

usage() {
  cat <<'EOF'
distsrv 一键部署脚本（Ubuntu 22.04 / 24.04）

用法：
  sudo ./deploy.sh [选项]

模式：
  生产模式（默认）：自动 HTTPS（Let's Encrypt），需要公网域名和 80/443 端口
  开发模式：仅 HTTP，监听 :8080，用于本机或内网测试

选项：
  --domain DOMAIN         分发域名（生产模式必填）
  --email EMAIL           Let's Encrypt 通知邮箱（生产模式必填）
  --admin-user USER       初始管理员用户名（默认 admin）
  --admin-pass PASS       初始管理员密码（留空则随机生成并显示）
  --org-name NAME         组织名（显示在页面页脚 / mobileconfig）
  --org-slug SLUG         组织 slug（用于 PayloadIdentifier，小写英文）
  --support EMAIL         技术支持联系邮箱
  --binary PATH           已构建的 distsrv 二进制路径（默认 ./distsrv）
  --build                 用 go build 在本机构建（需要已安装 Go 1.22+）
  --dev                   开发模式（仅 HTTP :8080，跳过域名/HTTPS）
  --skip-ufw              不修改 ufw 防火墙规则
  --no-systemd            不使用 systemd（用 nohup 直接启动，用于容器/无 systemd 环境）
  --yes, -y               非交互模式，使用提供的参数和默认值，不询问
  --help, -h              显示本帮助

示例：
  # 生产部署（交互模式，会问域名/邮箱）
  sudo ./deploy.sh

  # 生产部署（无交互）
  sudo ./deploy.sh --yes \
       --domain dist.example.com \
       --email ops@example.com \
       --org-name "ACME Inc" --org-slug "acme"

  # 开发模式（HTTP，:8080）
  sudo ./deploy.sh --dev --yes

  # 在没有预编译二进制时，让脚本调用 go build
  sudo ./deploy.sh --build --domain dist.example.com --email ops@example.com
EOF
  exit 0
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)       DOMAIN="${2:-}"; shift 2 ;;
    --email)        EMAIL="${2:-}"; shift 2 ;;
    --admin-user)   ADMIN_USER="${2:-}"; shift 2 ;;
    --admin-pass)   ADMIN_PASS="${2:-}"; shift 2 ;;
    --org-name)     ORG_NAME="${2:-}"; shift 2 ;;
    --org-slug)     ORG_SLUG="${2:-}"; shift 2 ;;
    --support)      SUPPORT="${2:-}"; shift 2 ;;
    --binary)       BINARY="${2:-}"; shift 2 ;;
    --build)        BUILD=1; shift ;;
    --dev)          DEV_MODE=1; shift ;;
    --skip-ufw)     SKIP_UFW=1; shift ;;
    --no-systemd)   NO_SYSTEMD=1; shift ;;
    --yes|-y)       ASSUME_YES=1; shift ;;
    --help|-h)      usage ;;
    *) fail "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

# ============================================================
# 1. 环境检查
# ============================================================
step "环境检查"
[[ $EUID -eq 0 ]] || fail "请以 root 或 sudo 运行：sudo $0 $*"

[[ -r /etc/os-release ]] || fail "缺少 /etc/os-release，无法识别系统"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  ubuntu|debian) echo "OS: $PRETTY_NAME" ;;
  *) warn "本脚本针对 Ubuntu/Debian 设计，检测到 $ID"; confirm "继续？" || exit 1 ;;
esac

ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)
echo "架构: $ARCH"

# ============================================================
# 2. 交互式补全参数（生产模式）
# ============================================================
if [[ "$DEV_MODE" -eq 0 ]]; then
  if [[ -z "$DOMAIN" ]]; then
    if [[ "$ASSUME_YES" -eq 1 ]]; then
      fail "生产模式需要 --domain，或加 --dev 切换到开发模式"
    fi
    read -rp "分发域名（例如 dist.example.com；留空切换开发模式）: " DOMAIN
    [[ -z "$DOMAIN" ]] && DEV_MODE=1
  fi
fi

if [[ "$DEV_MODE" -eq 0 ]]; then
  if [[ -z "$EMAIL" ]]; then
    if [[ "$ASSUME_YES" -eq 1 ]]; then
      EMAIL="admin@$DOMAIN"
      warn "未指定 --email，默认使用 $EMAIL"
    else
      read -rp "Let's Encrypt 通知邮箱: " EMAIL
    fi
  fi
  [[ -z "$EMAIL" ]] && fail "需要 --email"
fi

if [[ -z "$ADMIN_PASS" ]]; then
  ADMIN_PASS=$(head -c 18 /dev/urandom | base64 | tr -d '+/=' | head -c 16)
  ADMIN_PASS_GENERATED=1
else
  ADMIN_PASS_GENERATED=0
fi
[[ -z "$ORG_NAME" ]] && ORG_NAME="Internal"
[[ -z "$ORG_SLUG" ]] && ORG_SLUG="internal"
[[ -z "$SUPPORT"  ]] && SUPPORT="${EMAIL:-ops@localhost}"

# ============================================================
# 3. 找/构建二进制
# ============================================================
step "准备二进制"
if [[ "$BUILD" -eq 1 ]]; then
  command -v go >/dev/null || fail "未安装 Go，请先 apt install golang-go 或参考 https://go.dev/doc/install"
  echo "go build (CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH/amd64/amd64})"
  (
    cd "$SCRIPT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" \
      go build -ldflags='-s -w' -o distsrv ./cmd/distsrv
  )
  BINARY="$SCRIPT_DIR/distsrv"
fi
[[ -z "$BINARY" ]] && BINARY="$SCRIPT_DIR/distsrv"
[[ -f "$BINARY" ]] || fail "找不到二进制 $BINARY；请用 --binary 指定，或加 --build 让脚本编译"
echo "二进制: $BINARY ($(du -h "$BINARY" | cut -f1))"

# ============================================================
# 4. 安装系统依赖
# ============================================================
step "安装系统依赖"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl sqlite3 iproute2 openssl >/dev/null
echo "依赖已安装：ca-certificates curl sqlite3 iproute2 openssl"

# ============================================================
# 5. 创建用户与目录
# ============================================================
step "创建服务用户与目录"
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  echo "已创建用户：$SERVICE_USER"
else
  echo "用户 $SERVICE_USER 已存在，跳过"
fi
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 \
  "$DATA_DIR" "$DATA_DIR/certs" "$DATA_DIR/uploads"
install -d -o root -g "$SERVICE_USER" -m 0750 "$ETC_DIR"
echo "目录：$DATA_DIR  $ETC_DIR"

# ============================================================
# 6. 端口检查（生产模式）
# ============================================================
if [[ "$DEV_MODE" -eq 0 ]]; then
  step "检查 80 / 443 端口"
  conflict=0
  for p in 80 443; do
    line=$(ss -tlnp 2>/dev/null | awk -v re=":${p}\$" '$4 ~ re {print; exit}') || true
    if [[ -n "$line" ]] && ! echo "$line" | grep -q distsrv; then
      warn "$p 端口已被占用：$line"
      conflict=1
    fi
  done
  if [[ "$conflict" -eq 1 ]]; then
    warn "autocert 首次签发证书需要 80 端口可达。如有其它 web server，请先 systemctl disable --now nginx apache2"
    confirm "继续部署？" || exit 1
  else
    echo "80 / 443 空闲"
  fi
fi

# ============================================================
# 7. 写 config.toml
# ============================================================
step "写入配置文件"
CONFIG_PATH="$ETC_DIR/config.toml"
if [[ -f "$CONFIG_PATH" ]]; then
  BACKUP="${CONFIG_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
  cp -p "$CONFIG_PATH" "$BACKUP"
  warn "已有配置文件，备份到 $BACKUP"
fi

# 用 cat 写入，使用引号防止变量误展开导致格式问题
TMP_CONFIG=$(mktemp)
{
  echo "[server]"
  if [[ "$DEV_MODE" -eq 1 ]]; then
    echo "dev_mode = true"
    echo "dev_addr = \":8080\""
  else
    echo "domain = \"$DOMAIN\""
    echo "http_addr = \":80\""
    echo "https_addr = \":443\""
    echo "acme_email = \"$EMAIL\""
    echo "acme_cache_dir = \"$DATA_DIR/certs\""
  fi
  echo "read_timeout_seconds = 300"
  echo
  echo "[admin]"
  echo "username = \"$ADMIN_USER\""
  echo "password = \"$ADMIN_PASS\""
  echo
  echo "[storage]"
  echo "data_dir = \"$DATA_DIR\""
  echo "uploads_subdir = \"uploads\""
  echo "max_upload_mb = 300"
  echo "keep_versions_per_platform = 3"
  echo "low_disk_threshold_mb = 500"
  echo
  echo "[db]"
  echo "path = \"$DATA_DIR/distsrv.db\""
  echo "busy_timeout_ms = 5000"
  echo
  echo "[site]"
  echo "org_name = \"$ORG_NAME\""
  echo "org_slug = \"$ORG_SLUG\""
  echo "support_contact = \"$SUPPORT\""
  echo
  echo "[security]"
  echo "session_ttl_hours = 168"
  echo "bcrypt_cost = 10"
  echo
  echo "[log]"
  echo "level = \"info\""
} > "$TMP_CONFIG"
install -m 0640 -o root -g "$SERVICE_USER" "$TMP_CONFIG" "$CONFIG_PATH"
rm -f "$TMP_CONFIG"
echo "配置：$CONFIG_PATH"

# ============================================================
# 8. 安装二进制
# ============================================================
step "安装二进制到 $INSTALL_PREFIX/distsrv"
# 如果服务正在跑，先停一下避免 ETXTBSY
if systemctl is-active --quiet distsrv 2>/dev/null; then
  systemctl stop distsrv
fi
install -m 0755 "$BINARY" "$INSTALL_PREFIX/distsrv"

# ============================================================
# 9. systemd unit
# ============================================================
if [[ "$NO_SYSTEMD" -eq 1 ]]; then
  step "跳过 systemd unit 安装（--no-systemd）"
else
step "安装 systemd unit"
cat > /etc/systemd/system/distsrv.service <<EOF
[Unit]
Description=App Distribution Service (distsrv)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
ExecStart=$INSTALL_PREFIX/distsrv -config $CONFIG_PATH
Restart=on-failure
RestartSec=5s
WorkingDirectory=$DATA_DIR

AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA_DIR
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictRealtime=true
LockPersonality=true
PrivateDevices=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native

MemoryMax=300M
MemoryHigh=200M

StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
fi

# ============================================================
# 10. 防火墙
# ============================================================
if [[ "$SKIP_UFW" -ne 1 ]] && command -v ufw >/dev/null 2>&1; then
  step "配置 ufw 防火墙"
  ufw allow OpenSSH >/dev/null || true
  if [[ "$DEV_MODE" -eq 1 ]]; then
    ufw allow 8080/tcp >/dev/null || true
  else
    ufw allow 80/tcp  >/dev/null || true
    ufw allow 443/tcp >/dev/null || true
  fi
  if ! ufw status | grep -qi 'Status: active'; then
    yes | ufw enable >/dev/null
    echo "ufw 已启用"
  fi
  ufw status numbered | sed 's/^/  /'
elif [[ "$SKIP_UFW" -eq 1 ]]; then
  step "跳过 ufw 配置（--skip-ufw）"
fi

# ============================================================
# 11. 启动服务
# ============================================================
step "启动 distsrv"
if [[ "$NO_SYSTEMD" -eq 1 ]]; then
  # 容器/无 systemd 环境：用 nohup 直接拉起
  LOG_FILE="/var/log/distsrv.log"
  PID_FILE="/var/run/distsrv.pid"
  touch "$LOG_FILE" && chown "$SERVICE_USER:$SERVICE_USER" "$LOG_FILE"
  # 如果已经在跑，先停掉
  if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    kill "$(cat "$PID_FILE")" || true
    sleep 1
  fi
  # 用 setpriv 让 distsrv 用户拿到 CAP_NET_BIND_SERVICE（绑 80/443）
  if command -v setpriv >/dev/null 2>&1; then
    nohup setpriv --reuid="$SERVICE_USER" --regid="$SERVICE_USER" --clear-groups \
      --ambient-caps=+net_bind_service \
      "$INSTALL_PREFIX/distsrv" -config "$CONFIG_PATH" \
      >>"$LOG_FILE" 2>&1 &
  else
    # 没 setpriv（少数发行版），开发模式无需 80/443，可以直接 su
    nohup su -s /bin/sh "$SERVICE_USER" -c \
      "$INSTALL_PREFIX/distsrv -config $CONFIG_PATH" \
      >>"$LOG_FILE" 2>&1 &
  fi
  PID=$!
  echo "$PID" > "$PID_FILE"
  sleep 2
  if ! kill -0 "$PID" 2>/dev/null; then
    c_red "distsrv 启动失败。日志末尾："
    tail -n 30 "$LOG_FILE"
    exit 1
  fi
  echo "distsrv 已运行（pid=$PID, 日志=$LOG_FILE）"
else
  systemctl enable distsrv >/dev/null 2>&1
  systemctl restart distsrv

  sleep 2
  if ! systemctl is-active --quiet distsrv; then
    c_red "distsrv 启动失败。最近 30 行日志："
    journalctl -u distsrv -n 30 --no-pager
    exit 1
  fi
  echo "distsrv 已运行（pid=$(systemctl show -p MainPID --value distsrv))"
fi

# ============================================================
# 12. 健康检查
# ============================================================
step "健康检查"
if [[ "$DEV_MODE" -eq 1 ]]; then
  URL="http://localhost:8080/healthz"
else
  URL="https://$DOMAIN/healthz"
fi
echo "GET $URL"
OK=0
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  if curl -fsS --max-time 5 "$URL" >/dev/null 2>&1; then
    OK=1
    break
  fi
  sleep 2
done
if [[ "$OK" -eq 1 ]]; then
  c_green "健康检查通过"
else
  warn "健康检查未通过。"
  if [[ "$DEV_MODE" -eq 0 ]]; then
    echo "  生产模式首次启动需要 Let's Encrypt 签发证书（10-60 秒）。"
    echo "  请确认：DNS A 记录已指到本机，80/443 端口公网可达。"
  fi
  echo "  实时日志：journalctl -u distsrv -f"
fi

# ============================================================
# 13. 完成提示
# ============================================================
PUB_IP=$(curl -fsS --max-time 3 https://api.ipify.org 2>/dev/null || echo "<your-server-ip>")

cat <<EOF

$(c_bold "============================================================")
$(c_bold "  distsrv 部署完成")
$(c_bold "============================================================")
EOF

if [[ "$DEV_MODE" -eq 1 ]]; then
  cat <<EOF
  访问地址：  http://$PUB_IP:8080/
  管理后台：  http://$PUB_IP:8080/admin/login

  $(c_yellow "[开发模式] 仅 HTTP，iOS OTA 安装无法工作（Apple 强制 HTTPS）。")
  $(c_yellow "      生产部署请用：sudo ./deploy.sh --domain ... --email ...")
EOF
else
  cat <<EOF
  访问地址：  https://$DOMAIN/
  管理后台：  https://$DOMAIN/admin/login

  本机公网 IP：$PUB_IP
  请确认 DNS A 记录：$DOMAIN -> $PUB_IP
EOF
fi

cat <<EOF

  初始账号：  $ADMIN_USER
  初始密码：  $(c_bold "$ADMIN_PASS")
EOF
if [[ "$ADMIN_PASS_GENERATED" -eq 1 ]]; then
  c_yellow "  ↑ 此密码由脚本随机生成。请立刻记下，下次将不再显示。"
fi

cat <<EOF

  下一步：
   1. 登录后立即修改密码（仪表盘底部表单）
   2. 修改后从 $CONFIG_PATH 删掉 [admin].password 一行
   3. systemctl restart distsrv
   4. 新建应用：/admin/apps/new

  常用命令：
$(if [[ "$NO_SYSTEMD" -eq 1 ]]; then
cat <<INNER
   $(c_bold "tail -f /var/log/distsrv.log")
   $(c_bold "kill \$(cat /var/run/distsrv.pid) && sudo $0 ...")  # 重启
INNER
else
cat <<INNER
   $(c_bold "systemctl status distsrv")
   $(c_bold "journalctl -u distsrv -f")
   $(c_bold "systemctl restart distsrv")
INNER
fi)

  数据目录： $DATA_DIR
  配置文件： $CONFIG_PATH
$(c_bold "============================================================")
EOF
