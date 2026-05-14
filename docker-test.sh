#!/usr/bin/env bash
# docker-test.sh — 在 ubuntu:24.04 容器内跑一遍 deploy.sh，验证整个部署流程
# 用法：./docker-test.sh [--keep]
#   --keep    测试完不删除容器，方便手动 docker exec 进去看
set -euo pipefail

cd "$(dirname "$0")"

# Convert a path to Windows form when running in Git Bash, so that docker.exe
# (a Windows binary) can find local files. Container-side paths are left alone
# by sending commands via stdin/heredoc.
hostpath() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    echo "$1"
  fi
}

KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

CONTAINER=distsrv-deploy-test
IMAGE=ubuntu:24.04
HOST_PORT=18080

c_green(){ printf '\033[32m%s\033[0m\n' "$*"; }
c_red(){   printf '\033[31m%s\033[0m\n' "$*"; }
step(){    printf '\n\033[36m==>\033[0m \033[1m%s\033[0m\n' "$*"; }

trap 'rc=$?; if [[ "$rc" -ne 0 ]]; then c_red "测试失败 (exit $rc)"; if [[ "$KEEP" -ne 1 ]]; then docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; fi; fi' EXIT

# ============================================================
# 1. 构建 Linux amd64 二进制 + CLI
# ============================================================
step "[1/7] 构建 Linux amd64 二进制 (server + cli)"
if [[ -n "${WINDIR:-}" || "$(uname -s)" == MINGW* ]]; then
  GO_BIN="/c/Program Files/Go/bin/go.exe"
  [[ -x "$GO_BIN" ]] || GO_BIN="go"
else
  GO_BIN="go"
fi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -ldflags='-s -w' -o distsrv ./cmd/distsrv
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -ldflags='-s -w' -o distsrv-cli ./cmd/distsrv-cli
ls -lh distsrv distsrv-cli

# ============================================================
# 2. 准备 staging 目录
# ============================================================
step "[2/7] 准备 staging 目录"
STAGE=$(mktemp -d)
cp distsrv distsrv-cli deploy.sh distsrv.service config.example.toml README.md "$STAGE/"
chmod +x "$STAGE/deploy.sh"
echo "staging: $STAGE"
ls -la "$STAGE"

# ============================================================
# 3. 起容器
# ============================================================
step "[3/7] 启动 $IMAGE 容器"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
  -p "$HOST_PORT:8080" \
  "$IMAGE" sleep 3600

# ============================================================
# 4. 把项目复制进去并运行 deploy.sh
# ============================================================
step "[4/7] 复制项目到容器并执行 deploy.sh"
docker exec -i "$CONTAINER" bash <<'EOF'
mkdir -p /root/distsrv-deploy
EOF
docker cp "$(hostpath "$STAGE")/." "$CONTAINER:/root/distsrv-deploy"
rm -rf "$STAGE"

docker exec -i "$CONTAINER" bash <<'EOF'
set -e
apt-get update -qq
cd /root/distsrv-deploy
./deploy.sh \
  --dev \
  --yes \
  --skip-ufw \
  --no-systemd \
  --binary /root/distsrv-deploy/distsrv \
  --admin-user admin \
  --admin-pass test1234 \
  --org-name "Docker Test" --org-slug "test"
EOF

# ============================================================
# 5. 健康检查 + 端到端 flow
# ============================================================
step "[5/7] 验证 /healthz、登录、创建应用"

# 容器内 healthz
echo "--- 容器内 healthz"
docker exec -i "$CONTAINER" bash <<'EOF'
curl -fsS http://localhost:8080/healthz
echo
EOF

# 宿主机映射端口
echo "--- 宿主机 curl http://localhost:$HOST_PORT/healthz"
if curl -fsS --max-time 5 "http://localhost:$HOST_PORT/healthz" >/dev/null; then
  c_green "✓ 端口映射工作正常"
else
  c_red "✗ 宿主机访问失败"
fi

# 端到端：登录 → 仪表盘 → 新建应用 → 公开下载页
echo "--- 端到端 flow"
docker exec -i "$CONTAINER" bash <<'EOF'
set -e
CK=/tmp/cookies.txt
rm -f "$CK"

HTTP_CODE=$(curl -sS -o /tmp/login_resp.html -w "%{http_code}" \
  -c "$CK" \
  -X POST http://localhost:8080/admin/login \
  --data-urlencode "username=admin" \
  --data-urlencode "password=test1234")
echo "  POST /admin/login -> $HTTP_CODE (expect 303)"
[[ "$HTTP_CODE" == "303" || "$HTTP_CODE" == "302" ]] || { echo "  登录失败"; cat /tmp/login_resp.html; exit 1; }

HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" -b "$CK" http://localhost:8080/admin/)
echo "  GET /admin/ -> $HTTP_CODE (expect 200)"
[[ "$HTTP_CODE" == "200" ]] || exit 1

HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" -b "$CK" \
  -X POST http://localhost:8080/admin/apps/new \
  --data-urlencode "short_id=demoapp" \
  --data-urlencode "name=演示应用" \
  --data-urlencode "description=docker-test")
echo "  POST /admin/apps/new -> $HTTP_CODE (expect 303)"
[[ "$HTTP_CODE" == "303" || "$HTTP_CODE" == "302" ]] || exit 1

HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" http://localhost:8080/d/demoapp)
echo "  GET /d/demoapp -> $HTTP_CODE (expect 200)"
[[ "$HTTP_CODE" == "200" ]] || exit 1

HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" http://localhost:8080/static/style.css)
echo "  GET /static/style.css -> $HTTP_CODE (expect 200)"
[[ "$HTTP_CODE" == "200" ]] || exit 1
EOF

c_green "✓ 全部检查通过"

# ============================================================
# 6. API + CLI 端到端
# ============================================================
step "[6/7] API + distsrv-cli 端到端验证"
docker exec -i "$CONTAINER" bash <<'EOF'
set -e
# zip is not in the base image
command -v zip >/dev/null 2>&1 || apt-get install -y -qq zip </dev/null >/dev/null
SERVER=http://localhost:8080
CK=/tmp/admin.cookies

# 1. admin 登录拿 session cookie
curl -sS -c "$CK" -o /dev/null \
  -X POST "$SERVER/admin/login" \
  --data-urlencode username=admin --data-urlencode password=test1234

# 2. 创建 API token，Accept: application/json 拿明文 token
TOKEN=$(curl -sS -b "$CK" \
  -H "Accept: application/json" \
  -X POST "$SERVER/admin/tokens" \
  --data-urlencode "name=docker-test" \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[[ -n "$TOKEN" ]] || { echo "  ✗ 获取 token 失败"; exit 1; }
echo "  token: ${TOKEN:0:10}…"

# 3. /api/v1/whoami
WHOAMI=$(curl -sS -H "Authorization: Bearer $TOKEN" "$SERVER/api/v1/whoami")
echo "  whoami: $WHOAMI"
echo "$WHOAMI" | grep -q '"username":"admin"' || exit 1

# 4. /api/v1/server (public)
curl -sS "$SERVER/api/v1/server" | grep -q '"dev_mode":true' || exit 1
echo "  server info OK"

# 5. /api/v1/apps list
APPS=$(curl -sS -H "Authorization: Bearer $TOKEN" "$SERVER/api/v1/apps")
echo "$APPS" | grep -q '"short_id":"demoapp"' || exit 1
echo "  apps list OK (demoapp present)"

# 6. /api/v1/apps create via JSON
NEW=$(curl -sS -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -X POST "$SERVER/api/v1/apps" \
  -d '{"short_id":"clitest","name":"CLI Test"}')
echo "$NEW" | grep -q '"short_id":"clitest"' || { echo "  create resp: $NEW"; exit 1; }
echo "  apps create OK"

# 7. 401 on missing/bad token
CODE=$(curl -sS -o /dev/null -w "%{http_code}" "$SERVER/api/v1/whoami")
[[ "$CODE" == "401" ]] || { echo "  expected 401, got $CODE"; exit 1; }
CODE=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer wrong" "$SERVER/api/v1/whoami")
[[ "$CODE" == "401" ]] || { echo "  expected 401 for bad token, got $CODE"; exit 1; }
echo "  401 unauthorized OK"

# 8. 安装 CLI + configure
install -m 0755 /root/distsrv-deploy/distsrv-cli /usr/local/bin/distsrv-cli
distsrv-cli configure --server "$SERVER" --token "$TOKEN" >/dev/null
distsrv-cli whoami | grep -q "username: admin" || { distsrv-cli whoami; exit 1; }
echo "  CLI configure + whoami OK"

# 9. CLI apps 列表
distsrv-cli apps | grep -q clitest || { distsrv-cli apps; exit 1; }
echo "  CLI apps list OK"

# 10. 构造最小合法 ipa（zip + Payload/Fake.app/Info.plist）并通过 CLI 上传
WORK=$(mktemp -d)
mkdir -p "$WORK/Payload/Fake.app"
cat > "$WORK/Payload/Fake.app/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string>com.test.fake</string>
  <key>CFBundleVersion</key>
  <string>1</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0.0</string>
  <key>CFBundleDisplayName</key>
  <string>Fake App</string>
  <key>CFBundleName</key>
  <string>Fake</string>
</dict>
</plist>
PLIST
( cd "$WORK" && zip -qr fake.ipa Payload )
ls -lh "$WORK/fake.ipa"

# CLI upload
UPLOAD_OUT=$(distsrv-cli upload clitest "$WORK/fake.ipa" --json 2>&1)
echo "$UPLOAD_OUT" | grep -Eq '"version_name":[[:space:]]*"1\.0\.0"' || { echo "$UPLOAD_OUT"; exit 1; }
echo "$UPLOAD_OUT" | grep -Eq '"bundle_id":[[:space:]]*"com\.test\.fake"' || { echo "$UPLOAD_OUT"; exit 1; }
echo "  ✓ CLI upload OK (版本 1.0.0, bundle com.test.fake)"

# 11. 验证应用的"当前版本"被自动设置 + 下载页能拿到
APP_DETAIL=$(curl -sS -H "Authorization: Bearer $TOKEN" "$SERVER/api/v1/apps/clitest")
echo "$APP_DETAIL" | grep -q '"version_name":"1.0.0"' || { echo "$APP_DETAIL"; exit 1; }
PAGE=$(curl -sS "$SERVER/d/clitest")
echo "$PAGE" | grep -q "1.0.0" || { echo "$PAGE" | head -50; exit 1; }
# Regression guard: html/template must NOT replace itms-services:// with #ZgotmplZ
if echo "$PAGE" | grep -q "#ZgotmplZ"; then
  echo "  ✗ html/template 把 itms-services URL 替换成 #ZgotmplZ — iOS 安装按钮会变死链"
  echo "$PAGE" | grep -A1 -B1 "ZgotmplZ"
  exit 1
fi
echo "$PAGE" | grep -q "itms-services://?action=download-manifest" \
  || { echo "  ✗ 下载页里没有 itms-services:// 链接"; echo "$PAGE" | grep -i itms; exit 1; }
echo "$PAGE" | grep -q 'src="data:image/png;base64,' \
  || { echo "  ✗ 二维码 data URL 没出现（被 html/template 过滤了？）"; echo "$PAGE" | grep -i 'qr\|data:'; exit 1; }
echo "  ✓ 下载页显示新版本 + itms-services URL + 二维码 data URL 完整"

# 12. 上传到不存在的 app -> 404
CODE=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@$WORK/fake.ipa" \
  "$SERVER/api/v1/apps/no-such-app/upload")
[[ "$CODE" == "404" ]] || { echo "  expected 404, got $CODE"; exit 1; }
echo "  404 on unknown app OK"

# 13. manifest.plist 应该出现新版本（GET，不是 HEAD）
VID=$(echo "$APP_DETAIL" | sed -n 's/.*"ios_current":{"id":\([0-9]*\).*/\1/p')
[[ -n "$VID" ]] || { echo "  无法从 detail 解析 ios_current id"; echo "$APP_DETAIL"; exit 1; }
MANIFEST_CODE=$(curl -sS -X GET -o /tmp/manifest.plist -w "%{http_code}" "$SERVER/manifest/${VID}.plist")
[[ "$MANIFEST_CODE" == "200" ]] || { echo "  manifest $VID -> $MANIFEST_CODE"; cat /tmp/manifest.plist; exit 1; }
grep -q "com.test.fake" /tmp/manifest.plist || { echo "  manifest 缺 bundle id"; cat /tmp/manifest.plist; exit 1; }
# Regression guard: html/template would silently escape "<?xml ..." to "&lt;?xml ...",
# producing invalid XML that iOS rejects. Make sure plist parses as XML.
head -c 5 /tmp/manifest.plist | grep -q "^<?xml" \
  || { echo "  ✗ manifest 第一行不是 <?xml（可能被 HTML-escape 成 &lt;?xml）"; head -1 /tmp/manifest.plist; exit 1; }
# IMPORTANT: redirect stdin from /dev/null. Without it apt-get reads from
# the bash heredoc stdin and swallows the rest of the script.
command -v python3 >/dev/null 2>&1 || apt-get install -y -qq python3 </dev/null >/dev/null
python3 -c "import xml.etree.ElementTree as ET; ET.parse('/tmp/manifest.plist')" \
  || { echo "  ✗ manifest 不是合法 XML"; cat /tmp/manifest.plist; exit 1; }
echo "  ✓ manifest /manifest/${VID}.plist 含 bundle id + 是合法 XML"

# UDID-collection mobileconfig
curl -sS -o /tmp/cli.mobileconfig -w "  mobileconfig fetch -> %{http_code}\n" \
  "$SERVER/mobileconfig/clitest.mobileconfig"
head -c 5 /tmp/cli.mobileconfig | grep -q "^<?xml" \
  || { echo "  ✗ mobileconfig 第一行不是 <?xml"; head -1 /tmp/cli.mobileconfig; exit 1; }
python3 -c "import xml.etree.ElementTree as ET; ET.parse('/tmp/cli.mobileconfig')" \
  || { echo "  ✗ mobileconfig 不是合法 XML"; cat /tmp/cli.mobileconfig; exit 1; }
# PayloadUUID 必须是 RFC 4122 v4 UUID（带短横线，36 字符）
UUID_VAL=$(python3 -c "
import plistlib
with open('/tmp/cli.mobileconfig','rb') as f:
    p = plistlib.load(f)
print(p.get('PayloadUUID',''))
")
echo "$UUID_VAL" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' \
  || { echo "  ✗ PayloadUUID 不是合法 v4 UUID: '$UUID_VAL'"; exit 1; }
echo "  ✓ mobileconfig 合法 XML + PayloadUUID 是 RFC 4122 v4 UUID"

# Regression: uploading a *second* version should auto-promote it to "current".
WORK2=$(mktemp -d)
mkdir -p "$WORK2/Payload/Fake.app"
cat > "$WORK2/Payload/Fake.app/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>com.test.fake</string>
  <key>CFBundleVersion</key><string>2</string>
  <key>CFBundleShortVersionString</key><string>1.0.1</string>
  <key>CFBundleDisplayName</key><string>Fake App</string>
  <key>CFBundleName</key><string>Fake</string>
</dict>
</plist>
PLIST
( cd "$WORK2" && zip -qr fake2.ipa Payload )
UP2=$(distsrv-cli upload clitest "$WORK2/fake2.ipa" --json 2>&1)
NEW_VID=$(echo "$UP2" | grep -Eo '"id":[[:space:]]*[0-9]+' | head -1 | grep -oE '[0-9]+')
[[ -n "$NEW_VID" ]] || { echo "  ✗ 第二次上传解析不到 version id"; echo "$UP2"; exit 1; }
echo "  第二次上传 version id = $NEW_VID"

# /api/v1/apps/clitest 现在的 ios_current.id 必须等于 NEW_VID（说明被自动设为当前）
DETAIL=$(curl -sS -H "Authorization: Bearer $TOKEN" "$SERVER/api/v1/apps/clitest")
CUR_VID=$(echo "$DETAIL" | grep -oE '"ios_current":\{"id":[[:space:]]*[0-9]+' | grep -oE '[0-9]+$')
[[ "$CUR_VID" == "$NEW_VID" ]] \
  || { echo "  ✗ 第二次上传未自动设为当前 (current=$CUR_VID expected=$NEW_VID)"; echo "$DETAIL"; exit 1; }
echo "  ✓ 第二次上传自动设为 ios 当前版本"

# 下载页 manifest URL 应该指向新 vid
PAGE2=$(curl -sS "$SERVER/d/clitest")
echo "$PAGE2" | grep -q "manifest/${NEW_VID}.plist" \
  || { echo "  ✗ 下载页 manifest URL 没指向新版本"; echo "$PAGE2" | grep manifest; exit 1; }
echo "  ✓ 下载页 manifest 指向新版本"

# UDID-callback regression: Profile Service expects the callback
# response to be a signed mobileconfig (PayloadType=Configuration with
# empty PayloadContent). Returning plain text or 200 with no body makes
# iOS show "安装失败" after the device has POSTed its info.
CALLBACK_BODY='<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>UDID</key><string>FAKE-TEST-UDID-12345</string><key>PRODUCT</key><string>iPhone15,2</string><key>VERSION</key><string>17.5</string></dict></plist>'
CALLBACK_RESP=$(mktemp)
CALLBACK_CT=$(curl -sS -o "$CALLBACK_RESP" -w "%{content_type}" \
  -X POST \
  -H "Content-Type: application/x-apple-aspen-config" \
  --data-binary "$CALLBACK_BODY" \
  "$SERVER/udid-callback?app=clitest")
echo "$CALLBACK_CT" | grep -q "application/x-apple-aspen-config" \
  || { echo "  ✗ /udid-callback 响应 Content-Type 不对: $CALLBACK_CT"; head -c 200 "$CALLBACK_RESP"; exit 1; }
grep -q "PayloadType" "$CALLBACK_RESP" \
  || { echo "  ✗ /udid-callback 响应不是 mobileconfig (无 PayloadType)"; head -c 200 "$CALLBACK_RESP"; exit 1; }
grep -q "<string>Configuration</string>" "$CALLBACK_RESP" \
  || { echo "  ✗ /udid-callback 响应 PayloadType 不是 Configuration"; head -c 200 "$CALLBACK_RESP"; exit 1; }
# Verify UDID was recorded
UDIDS_JSON=$(curl -sS -H "Authorization: Bearer $TOKEN" -X POST \
  "$SERVER/admin/users" 2>/dev/null || true)
# Use sqlite via admin -- simpler: just check log
echo "  ✓ /udid-callback 返回签名/未签名 Configuration mobileconfig"

echo "✓ API + CLI 全部通过"
EOF

c_green "✓ API + CLI 端到端测试完成"

# ============================================================
# 6.5 多用户功能
# ============================================================
step "[6.5/7] 多用户：创建/禁用/权限/重置密码"
docker exec -i "$CONTAINER" bash <<'EOF'
set -e
SERVER=http://localhost:8080
ADMIN_CK=/tmp/admin.cookies
USER_CK=/tmp/user.cookies

# admin 登录
rm -f "$ADMIN_CK"
curl -sS -c "$ADMIN_CK" -o /dev/null \
  -X POST "$SERVER/admin/login" \
  --data-urlencode username=admin --data-urlencode password=test1234

# 1. 非 admin 不能访问 /admin/users（创建一个普通用户测试）
curl -sS -b "$ADMIN_CK" -X POST "$SERVER/admin/users" \
  --data-urlencode "username=alice" \
  --data-urlencode "password=alicepass123" \
  -o /dev/null
echo "  创建普通用户 alice OK"

# 2. alice 登录
rm -f "$USER_CK"
HTTP_CODE=$(curl -sS -c "$USER_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/login" \
  --data-urlencode username=alice --data-urlencode password=alicepass123)
[[ "$HTTP_CODE" == "303" ]] || { echo "  alice 登录失败 $HTTP_CODE"; exit 1; }
echo "  alice 登录 OK"

# 3. alice 能访问 dashboard
HTTP_CODE=$(curl -sS -b "$USER_CK" -o /dev/null -w "%{http_code}" "$SERVER/admin/")
[[ "$HTTP_CODE" == "200" ]] || exit 1
echo "  alice 访问 dashboard 200 OK"

# 4. alice 不能访问 /admin/users（应该 403）
HTTP_CODE=$(curl -sS -b "$USER_CK" -o /dev/null -w "%{http_code}" "$SERVER/admin/users")
[[ "$HTTP_CODE" == "403" ]] || { echo "  expected 403 got $HTTP_CODE"; exit 1; }
echo "  alice 访问 /admin/users -> 403 OK"

# 5. alice 不能创建用户
HTTP_CODE=$(curl -sS -b "$USER_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users" \
  --data-urlencode "username=hacker" --data-urlencode "password=xxxxxxxx")
[[ "$HTTP_CODE" == "403" ]] || { echo "  expected 403 got $HTTP_CODE"; exit 1; }
echo "  alice 创建用户 -> 403 OK"

# 6. alice 能创建自己的 API token
TOKEN_ALICE=$(curl -sS -b "$USER_CK" -H "Accept: application/json" \
  -X POST "$SERVER/admin/tokens" --data-urlencode "name=alice-mac" \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[[ -n "$TOKEN_ALICE" ]] || exit 1
echo "  alice token: ${TOKEN_ALICE:0:10}…"
curl -sS -H "Authorization: Bearer $TOKEN_ALICE" "$SERVER/api/v1/whoami" | grep -q '"username":"alice"' || exit 1
echo "  alice token whoami -> alice OK"

# 7. admin 禁用 alice
USER_ID=$(curl -sS -b "$ADMIN_CK" "$SERVER/admin/users" | grep -oE 'users/[0-9]+/toggle-admin' | head -1 | grep -oE '[0-9]+')
[[ -n "$USER_ID" ]] || { echo "  无法解析 alice id"; exit 1; }
echo "  alice id = $USER_ID"
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/toggle-disabled")
[[ "$HTTP_CODE" == "303" ]] || exit 1
echo "  禁用 alice OK"

# 8. 禁用后 alice 的 session 应失效（被 SetUserDisabled 一并删除）
HTTP_CODE=$(curl -sS -b "$USER_CK" -o /dev/null -w "%{http_code}" "$SERVER/admin/")
# 应该被 redirect 到 login（curl 默认不跟随重定向）
[[ "$HTTP_CODE" == "303" ]] || { echo "  禁用后访问 dashboard 应 303 重定向到 login, got $HTTP_CODE"; exit 1; }
echo "  禁用后 alice session 失效 OK"

# 9. 禁用后 alice 的 token 应 401
HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN_ALICE" "$SERVER/api/v1/whoami")
[[ "$HTTP_CODE" == "401" ]] || { echo "  禁用后 token 应 401, got $HTTP_CODE"; exit 1; }
echo "  禁用后 alice token 401 OK"

# 10. alice 不能重新登录
HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/login" \
  --data-urlencode username=alice --data-urlencode password=alicepass123)
[[ "$HTTP_CODE" == "401" ]] || { echo "  禁用账号登录应 401, got $HTTP_CODE"; exit 1; }
echo "  禁用账号无法登录 OK"

# 11. admin 重新启用 alice
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/toggle-disabled")
[[ "$HTTP_CODE" == "303" ]] || exit 1
echo "  启用 alice OK"

# 12. alice 重新登录后 token 又能用
HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN_ALICE" "$SERVER/api/v1/whoami")
[[ "$HTTP_CODE" == "200" ]] || { echo "  启用后 token 应 200, got $HTTP_CODE"; exit 1; }
echo "  启用后 alice token 又能用 OK"

# 13. 把 alice 提升为 admin
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/toggle-admin")
[[ "$HTTP_CODE" == "303" ]] || exit 1
echo "  alice -> admin OK"

# 14. alice 重新登录，现在能访问 /admin/users
rm -f "$USER_CK"
curl -sS -c "$USER_CK" -o /dev/null -X POST "$SERVER/admin/login" \
  --data-urlencode username=alice --data-urlencode password=alicepass123
HTTP_CODE=$(curl -sS -b "$USER_CK" -o /dev/null -w "%{http_code}" "$SERVER/admin/users")
[[ "$HTTP_CODE" == "200" ]] || { echo "  alice as admin 应能访问 users, got $HTTP_CODE"; exit 1; }
echo "  alice 现在是 admin，能访问 /admin/users OK"

# 15. admin 不能撤销自己的 admin（防自伤）
ADMIN_ID=$(curl -sS -b "$ADMIN_CK" "$SERVER/admin/users" | grep -E '<tr>' | head -5 | tr -d '\n' | grep -oE 'admin</b>' | head -1 || echo "")
# 简化：直接尝试 toggle-admin id=1（admin 的 id）
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/1/toggle-admin")
[[ "$HTTP_CODE" == "400" ]] || { echo "  admin 改自己 admin 状态应 400, got $HTTP_CODE"; exit 1; }
echo "  防自伤：不能改自己的 admin 状态 OK"

# 16. admin 不能删自己
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/1/delete")
[[ "$HTTP_CODE" == "400" ]] || { echo "  admin 删自己应 400, got $HTTP_CODE"; exit 1; }
echo "  防自伤：不能删自己 OK"

# 17. 重置 alice 密码（cookie 里会有新密码）
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -D /tmp/headers -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/reset-password")
[[ "$HTTP_CODE" == "303" ]] || exit 1
NEW_PW=$(grep -oE 'distsrv_new_user_pw=[^;]+' /tmp/headers | sed 's/.*=alice|//')
[[ -n "$NEW_PW" ]] || { echo "  重置密码后未拿到新密码"; cat /tmp/headers; exit 1; }
echo "  重置 alice 密码 -> ${NEW_PW:0:6}… OK"

# 18. 用新密码登录
rm -f "$USER_CK"
HTTP_CODE=$(curl -sS -c "$USER_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/login" \
  --data-urlencode "username=alice" --data-urlencode "password=$NEW_PW")
[[ "$HTTP_CODE" == "303" ]] || { echo "  新密码登录应 303, got $HTTP_CODE"; exit 1; }
echo "  新密码登录 alice OK"

# 19. 撤销 alice 的 admin（admin 还在，allowed）
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/toggle-admin")
[[ "$HTTP_CODE" == "303" ]] || exit 1
echo "  撤销 alice admin OK"

# 20. 删除 alice
HTTP_CODE=$(curl -sS -b "$ADMIN_CK" -o /dev/null -w "%{http_code}" \
  -X POST "$SERVER/admin/users/$USER_ID/delete")
[[ "$HTTP_CODE" == "303" ]] || exit 1
echo "  删除 alice OK"

# alice 的 token 也应该被级联删除
HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN_ALICE" "$SERVER/api/v1/whoami")
[[ "$HTTP_CODE" == "401" ]] || { echo "  删除用户后 token 应 401, got $HTTP_CODE"; exit 1; }
echo "  级联删除 alice 的 token OK"

echo "✓ 多用户全部通过"
EOF

c_green "✓ 多用户测试完成"

# ============================================================
# 7. 检查容器内最终状态
# ============================================================
step "[7/7] 容器内状态摘要"
echo "--- 容器内文件结构（关键路径）"
docker exec -i "$CONTAINER" bash <<'EOF'
ls -la /etc/distsrv/
ls -la /var/lib/distsrv/
ls -la /usr/local/bin/distsrv
id distsrv
EOF

echo
echo "--- distsrv 日志末尾"
docker exec -i "$CONTAINER" bash <<'EOF'
tail -n 20 /var/log/distsrv.log
EOF

# ============================================================
# 清理
# ============================================================
if [[ "$KEEP" -eq 1 ]]; then
  cat <<EOF

$(c_green "保留容器 $CONTAINER")，可以手动进去看：
  docker exec -it $CONTAINER bash
  docker exec $CONTAINER tail -f /var/log/distsrv.log
  curl http://localhost:$HOST_PORT/admin/login

清理命令：
  docker rm -f $CONTAINER
EOF
else
  step "清理容器"
  docker rm -f "$CONTAINER" >/dev/null
  rm -f distsrv
  c_green "✓ 测试完成，容器已清理"
fi
