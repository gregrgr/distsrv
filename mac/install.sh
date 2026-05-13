#!/usr/bin/env bash
# install.sh — 把同目录的 distsrv-cli 安装到 /usr/local/bin
# 用法：./install.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_NAME="distsrv-cli"
SRC="$SCRIPT_DIR/$BIN_NAME"
DEST="/usr/local/bin/$BIN_NAME"

if [[ ! -f "$SRC" ]]; then
  echo "错误：找不到二进制 $SRC"
  echo "请确认你解压的是和当前 Mac 架构匹配的 tarball：" >&2
  echo "  Apple Silicon (M1/M2/M3): distsrv-cli-darwin-arm64.tar.gz" >&2
  echo "  Intel Mac:                 distsrv-cli-darwin-amd64.tar.gz" >&2
  exit 1
fi

# Apple Silicon detection
ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) WANT=arm64 ;;
  x86_64|amd64)  WANT=amd64 ;;
  *) WANT=unknown ;;
esac

if file -b "$SRC" | grep -q arm64 && [[ "$WANT" != "arm64" ]]; then
  echo "警告：CLI 是 arm64，但当前 Mac 是 $ARCH。可能跑不起来。"
fi
if file -b "$SRC" | grep -q x86_64 && [[ "$WANT" != "amd64" ]]; then
  echo "警告：CLI 是 amd64，但当前 Mac 是 $ARCH。可能跑不起来（除非走 Rosetta）。"
fi

# 检查是否需要 sudo
if [[ -w "$(dirname "$DEST")" ]]; then
  install -m 0755 "$SRC" "$DEST"
else
  echo "需要 sudo 写入 $DEST"
  sudo install -m 0755 "$SRC" "$DEST"
fi

# macOS Gatekeeper：去掉隔离属性，避免首次运行被拦
if command -v xattr >/dev/null 2>&1; then
  if [[ -w "$DEST" ]]; then
    xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
  else
    sudo xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
  fi
fi

echo "✓ 已安装到 $DEST"
echo
echo "下一步："
echo "  distsrv-cli configure --server https://your.dist.example.com --token <token>"
echo "  distsrv-cli apps"
echo
echo "在 admin 后台 /admin/tokens 创建 token。"
