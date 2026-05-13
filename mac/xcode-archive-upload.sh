#!/usr/bin/env bash
# xcode-archive-upload.sh
# 在 Xcode 完成 Archive 后自动导出 .ipa 并上传到 distsrv。
#
# 三种使用方式：
#
# 1) Xcode Scheme → Archive → Post-actions
#    Provide build settings from: <你的 iOS target>
#    Shell: /bin/bash
#    Script body:
#        "${SRCROOT}/../scripts/xcode-archive-upload.sh"
#    Xcode 会自动设置环境变量 ARCHIVE_PATH / PRODUCT_NAME / ...
#
# 2) 终端命令（手动 archive）
#        export ARCHIVE_PATH="$HOME/Library/Developer/Xcode/Archives/.../MyApp.xcarchive"
#        export DISTSRV_APP_SHORT_ID="myapp"
#        ./xcode-archive-upload.sh
#
# 3) CI（xcodebuild）
#        xcodebuild archive -scheme MyApp -archivePath ./MyApp.xcarchive ...
#        ARCHIVE_PATH=./MyApp.xcarchive DISTSRV_APP_SHORT_ID=myapp \
#          ./xcode-archive-upload.sh

set -euo pipefail

# ---- 必需环境变量 ----
: "${ARCHIVE_PATH:?Xcode 会自动设置，CI 请显式 export}"
: "${DISTSRV_APP_SHORT_ID:?请在 Scheme Post-action 或环境中设置目标应用 short_id}"

# ---- 可选配置 ----
EXPORT_OPTIONS_PLIST="${DISTSRV_EXPORT_OPTIONS:-$(dirname "$0")/exportOptions-adhoc.plist}"
EXPORT_DIR="${DISTSRV_EXPORT_DIR:-${HOME}/Library/Developer/Xcode/DistSrv-Export}"
OPEN_BROWSER="${DISTSRV_OPEN:-1}"   # 1 = 上传完自动打开下载页

CLI="${DISTSRV_CLI:-distsrv-cli}"

if ! command -v "$CLI" >/dev/null 2>&1; then
  echo "错误：找不到 $CLI。先装 CLI：" >&2
  echo "  解压 distsrv-cli-darwin-*.tar.gz 后 sudo ./install.sh" >&2
  exit 1
fi
if [[ ! -f "$EXPORT_OPTIONS_PLIST" ]]; then
  echo "错误：找不到 exportOptions plist：$EXPORT_OPTIONS_PLIST" >&2
  echo "  参考同目录的 exportOptions-adhoc.plist 自行调整 teamID 和签名信息" >&2
  exit 1
fi

mkdir -p "$EXPORT_DIR"
STAGE="$(mktemp -d "$EXPORT_DIR/run.XXXXXX")"

echo "==> 从 archive 导出 .ipa"
echo "    archive: $ARCHIVE_PATH"
echo "    options: $EXPORT_OPTIONS_PLIST"
xcodebuild -exportArchive \
  -archivePath "$ARCHIVE_PATH" \
  -exportOptionsPlist "$EXPORT_OPTIONS_PLIST" \
  -exportPath "$STAGE" \
  -allowProvisioningUpdates

IPA_FILE=$(find "$STAGE" -maxdepth 2 -name '*.ipa' -print -quit || true)
if [[ -z "$IPA_FILE" ]]; then
  echo "错误：导出未产出 .ipa（看 Xcode 日志）" >&2
  exit 2
fi
echo "==> 找到 IPA：$IPA_FILE"

echo "==> 上传到 distsrv"
OPEN_FLAG=()
[[ "$OPEN_BROWSER" == "1" ]] && OPEN_FLAG=(--open)
"$CLI" upload "$DISTSRV_APP_SHORT_ID" "$IPA_FILE" "${OPEN_FLAG[@]}"

# 通知中心提醒（可选）
if command -v osascript >/dev/null 2>&1; then
  osascript -e "display notification \"已上传到 distsrv: $DISTSRV_APP_SHORT_ID\" with title \"distsrv\" sound name \"Glass\"" >/dev/null 2>&1 || true
fi

echo "==> 完成"
