#!/usr/bin/env bash
# release-local.sh — 在本机一键发布 (不依赖 GitHub Actions)
#
# 流程：
#   1. 校验 git 干净 + tag 合法
#   2. make release-all VERSION=<tag>  (产出 dist/*.tar.gz + SHA256SUMS.txt)
#   3. git tag <tag> && git push --tags  (除非 --no-tag)
#   4. gh release create <tag> + 上传 artifacts
#
# 用法：
#   ./scripts/release-local.sh v0.1.0
#   ./scripts/release-local.sh v0.1.0 --draft        # 创建草稿 release
#   ./scripts/release-local.sh v0.1.0 --no-tag       # tag 已存在
#   ./scripts/release-local.sh v0.1.0 --prerelease   # 标记 pre-release

set -euo pipefail

cd "$(dirname "$0")/.."

# ---- parse args ----
TAG=""
DRAFT=0
PRERELEASE=0
DO_TAG=1
REPO=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --draft) DRAFT=1; shift ;;
    --prerelease) PRERELEASE=1; shift ;;
    --no-tag) DO_TAG=0; shift ;;
    --repo) REPO="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,/^set -e/p' "$0" | head -n 20 | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*) echo "unknown flag: $1"; exit 1 ;;
    *)
      if [[ -z "$TAG" ]]; then TAG="$1"; shift
      else echo "unexpected positional arg: $1"; exit 1
      fi
      ;;
  esac
done

if [[ -z "$TAG" ]]; then
  echo "用法：./scripts/release-local.sh <tag> [--draft] [--prerelease] [--no-tag]"
  exit 2
fi
if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
  echo "错误：tag 必须是 vMAJOR.MINOR.PATCH 形式，例如 v0.1.0、v1.2.3-rc1"
  exit 2
fi

# ---- preflight ----
echo "==> [1/5] preflight"
command -v gh >/dev/null 2>&1 || { echo "缺少 gh（GitHub CLI）。装：brew install gh / apt install gh"; exit 1; }
command -v go >/dev/null 2>&1 || { echo "缺少 go"; exit 1; }
command -v git >/dev/null 2>&1 || { echo "缺少 git"; exit 1; }

if ! gh auth status >/dev/null 2>&1; then
  echo "未登录 gh。先跑：gh auth login"
  exit 1
fi

# 校验工作树干净（避免发布脏代码）
if [[ -n "$(git status --porcelain)" ]]; then
  echo "错误：工作树有未提交改动。先 commit 或 stash。"
  git status --short
  exit 1
fi

# 检查 tag 是否存在
if git rev-parse "$TAG" >/dev/null 2>&1; then
  if [[ "$DO_TAG" -eq 1 ]]; then
    echo "错误：tag $TAG 已存在。加 --no-tag 跳过创建 tag，或换一个版本。"
    exit 1
  fi
fi

# 拿 repo（owner/name），优先 --repo 参数
if [[ -z "$REPO" ]]; then
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || echo "")
  if [[ -z "$REPO" ]]; then
    echo "无法解析仓库 owner/name。用 --repo owner/name 指定。"
    exit 1
  fi
fi
echo "  repo:    $REPO"
echo "  tag:     $TAG"
echo "  draft:   $([[ $DRAFT -eq 1 ]] && echo yes || echo no)"
echo "  prerel:  $([[ $PRERELEASE -eq 1 ]] && echo yes || echo no)"

# ---- build ----
echo
echo "==> [2/5] go vet + test"
go vet ./...
go test ./...

echo
echo "==> [3/5] make release-all"
make release-all VERSION="$TAG"
ls -lh dist/

# ---- tag ----
if [[ "$DO_TAG" -eq 1 ]]; then
  echo
  echo "==> [4/5] git tag + push"
  git tag -a "$TAG" -m "Release $TAG"
  git push origin "$TAG"
else
  echo
  echo "==> [4/5] 跳过 tag（--no-tag）"
fi

# ---- gh release ----
echo
echo "==> [5/5] gh release create"
RELEASE_FLAGS=()
[[ "$DRAFT" -eq 1 ]]      && RELEASE_FLAGS+=(--draft)
[[ "$PRERELEASE" -eq 1 ]] && RELEASE_FLAGS+=(--prerelease)

# release notes：从 git log 自动生成 + 一段 install 说明
NOTES=$(mktemp)
trap 'rm -f $NOTES' EXIT
{
  echo "## Install"
  echo
  echo "### Server (Ubuntu 22.04 / 24.04)"
  echo '```bash'
  echo "curl -fsSL https://github.com/$REPO/releases/download/$TAG/distsrv-$TAG-linux-amd64.tar.gz | tar -xz -C ~/distsrv-deploy"
  echo 'cd ~/distsrv-deploy && sudo ./deploy.sh'
  echo '```'
  echo
  echo "### Mac CLI"
  echo '```bash'
  echo "curl -fsSL https://raw.githubusercontent.com/$REPO/$TAG/mac/install-remote.sh | bash"
  echo '```'
  echo
  echo "### Verify checksums"
  echo '```bash'
  echo "curl -fsSL https://github.com/$REPO/releases/download/$TAG/SHA256SUMS.txt | sha256sum -c --ignore-missing"
  echo '```'
  echo
  echo "## Changelog"
  echo
  # last tag → HEAD changelog
  PREV=$(git describe --tags --abbrev=0 "$TAG^" 2>/dev/null || true)
  if [[ -n "$PREV" ]]; then
    git log --pretty='* %s (%h)' "$PREV..$TAG"
  else
    git log --pretty='* %s (%h)' -n 50
  fi
} > "$NOTES"

gh release create "$TAG" \
  --repo "$REPO" \
  --title "$TAG" \
  --notes-file "$NOTES" \
  "${RELEASE_FLAGS[@]}" \
  dist/distsrv-*-linux-amd64.tar.gz \
  dist/distsrv-*-linux-arm64.tar.gz \
  dist/distsrv-cli-*-darwin-arm64.tar.gz \
  dist/distsrv-cli-*-darwin-amd64.tar.gz \
  dist/distsrv-cli-*-linux-amd64.tar.gz \
  dist/distsrv-cli-*-linux-arm64.tar.gz \
  dist/distsrv-cli-*-windows-amd64.tar.gz \
  dist/SHA256SUMS.txt

echo
echo "✓ 发布完成：https://github.com/$REPO/releases/tag/$TAG"
