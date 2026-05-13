#!/usr/bin/env bash
# install-remote.sh — 在 Mac 上一行安装 distsrv-cli（从 GitHub Release 自动下载对应架构）
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/<branch>/mac/install-remote.sh | bash
#
# 可选环境变量：
#   DISTSRV_REPO    owner/repo  (例如 myorg/distsrv，必填)
#   DISTSRV_TAG     v0.1.0       (默认 latest)
#   PREFIX          /usr/local/bin (安装目录，需要写权限或会用 sudo)
#
# 如果你 fork 了仓库，请改 DISTSRV_REPO 或者在 raw URL 里写 fork 的路径。

set -euo pipefail

REPO="${DISTSRV_REPO:-gregrgr/distsrv}"
TAG="${DISTSRV_TAG:-latest}"
PREFIX="${PREFIX:-/usr/local/bin}"

# ---- detect platform ----
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin) ;;
  linux)  ;;
  *) echo "不支持的系统：$OS（本脚本仅支持 macOS 和 Linux）"; exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) echo "不支持的架构：$ARCH"; exit 1 ;;
esac

echo "==> 平台：$OS/$ARCH"
echo "==> 仓库：$REPO"

# ---- resolve tag ----
if [[ "$TAG" == "latest" ]]; then
  echo "==> 查询最新 release"
  TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  [[ -n "$TAG" ]] || { echo "无法解析最新 tag"; exit 1; }
fi
echo "==> 版本：$TAG"

ASSET="distsrv-cli-${TAG}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
echo "==> 下载：$URL"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if ! curl -fsSL "$URL" -o "$TMP/asset.tar.gz"; then
  cat <<EOF >&2
下载失败。可能原因：
  1. 该版本没有 $OS/$ARCH 资产
  2. tag 不存在
  3. 仓库私有，请先 gh auth login + 用 gh 下载
看一眼可用资产：https://github.com/$REPO/releases/tag/$TAG
EOF
  exit 1
fi

# ---- verify checksum if SHA256SUMS.txt exists ----
if curl -fsSL "https://github.com/$REPO/releases/download/$TAG/SHA256SUMS.txt" -o "$TMP/SHA256SUMS.txt" 2>/dev/null; then
  echo "==> 校验 sha256"
  EXPECTED=$(grep " ${ASSET}\$" "$TMP/SHA256SUMS.txt" | awk '{print $1}')
  if [[ -n "$EXPECTED" ]]; then
    if command -v sha256sum >/dev/null 2>&1; then
      ACTUAL=$(sha256sum "$TMP/asset.tar.gz" | awk '{print $1}')
    else
      ACTUAL=$(shasum -a 256 "$TMP/asset.tar.gz" | awk '{print $1}')
    fi
    if [[ "$EXPECTED" != "$ACTUAL" ]]; then
      echo "sha256 不匹配！expected=$EXPECTED got=$ACTUAL"
      exit 1
    fi
    echo "    OK ($EXPECTED)"
  fi
fi

# ---- extract + install ----
echo "==> 解压"
tar -xzf "$TMP/asset.tar.gz" -C "$TMP"
ls "$TMP"

SRC="$TMP/distsrv-cli"
[[ -f "$SRC" ]] || { echo "tarball 里没找到 distsrv-cli"; ls "$TMP"; exit 1; }
chmod +x "$SRC"

DEST="$PREFIX/distsrv-cli"
echo "==> 安装到 $DEST"
if [[ -w "$(dirname "$DEST")" ]]; then
  install -m 0755 "$SRC" "$DEST"
else
  sudo install -m 0755 "$SRC" "$DEST"
fi

# Strip macOS Gatekeeper quarantine
if [[ "$OS" == "darwin" ]] && command -v xattr >/dev/null 2>&1; then
  ( [[ -w "$DEST" ]] && xattr -d com.apple.quarantine "$DEST" 2>/dev/null ) \
    || sudo xattr -d com.apple.quarantine "$DEST" 2>/dev/null \
    || true
fi

echo
echo "✓ 安装完成：$DEST"
"$DEST" version || true
echo
echo "下一步："
echo "  distsrv-cli configure --server https://your.dist.example.com --token <token>"
echo "  distsrv-cli apps"
echo "  distsrv-cli upload myapp ./MyApp.ipa --open"
echo
echo "Token 在 admin 后台 /admin/tokens 创建。"
