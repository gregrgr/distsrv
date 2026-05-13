# distsrv-cli — Mac 端快捷发布

让 iOS / Android 开发者在 Mac 上一条命令把 .ipa / .apk 发布到 distsrv。
也支持 Xcode Archive 后自动 export ipa → 上传 → 浏览器打开下载页一气呵成。

## 1. 安装

### 1.1 在管理机上构建（一次）

```bash
make build-cli-mac        # 生成 dist/distsrv-cli-darwin-{arm64,amd64}.tar.gz
```

### 1.2 在每台 Mac 上

把对应架构的 tarball 给同事（U 盘 / scp / 邮件均可），然后：

```bash
tar -xzf distsrv-cli-darwin-arm64.tar.gz       # M1/M2/M3 用 arm64
# tar -xzf distsrv-cli-darwin-amd64.tar.gz     # Intel Mac 用 amd64
cd distsrv-cli-darwin-*
sudo ./install.sh
```

完成。`distsrv-cli` 已在 `/usr/local/bin/`。

## 2. 配置

在管理后台 `https://你的服务器/admin/tokens` 点"创建新 Token"，把生成的 token 复制下来（只显示一次！），然后：

```bash
distsrv-cli configure \
  --server https://dist.example.com \
  --token  dst_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

配置写入 `~/.config/distsrv-cli/config.toml`。

验证：

```bash
distsrv-cli whoami
distsrv-cli apps
```

## 3. 上传 IPA / APK

```bash
distsrv-cli upload myapp ./build/MyApp.ipa
distsrv-cli upload myapp ./build/MyApp.apk --open    # --open: 上传后自动打开下载页
```

输出示例：

```
上传 MyApp.ipa (87.3 MB) → https://dist.example.com/myapp ...
  [██████████████████████████████] 100.0%  87.3/87.3 MB
✓ 上传成功
版本：1.2.3 (42)
大小：87.3 MB
下载页：https://dist.example.com/d/myapp
文件 URL：https://dist.example.com/file/12/myapp-...ipa
```

## 4. Xcode Archive 后自动发布

### 4.1 准备

1. 编辑 `mac/exportOptions-adhoc.plist`，把 `YOUR_TEAM_ID` 改成你的 Apple Developer Team ID
2. 把 `mac/xcode-archive-upload.sh` 和 `exportOptions-adhoc.plist` 放到 iOS 项目里（建议 `scripts/`）

### 4.2 方式一：Xcode Scheme Post-action（手动 Archive 触发）

1. Xcode → Product → Scheme → Edit Scheme
2. 左栏选 **Archive** → **Post-actions**
3. 点 `+` → **New Run Script Action**
4. 顶部 **Provide build settings from** 选你的 iOS target（这样 Xcode 会注入 `ARCHIVE_PATH` 等环境变量）
5. Shell：`/bin/bash`
6. Script body：
   ```bash
   export DISTSRV_APP_SHORT_ID="myapp"
   export DISTSRV_EXPORT_OPTIONS="${SRCROOT}/scripts/exportOptions-adhoc.plist"
   "${SRCROOT}/scripts/xcode-archive-upload.sh"
   ```

之后每次 Product → Archive，完成后会自动 export ipa、上传到 distsrv、弹出通知、打开下载页。

### 4.3 方式二：CI（xcodebuild + 一条命令）

```bash
# 1. archive
xcodebuild archive \
  -scheme MyApp \
  -configuration Release \
  -archivePath ./build/MyApp.xcarchive \
  -destination "generic/platform=iOS"

# 2. export + upload
ARCHIVE_PATH=./build/MyApp.xcarchive \
DISTSRV_APP_SHORT_ID=myapp \
DISTSRV_OPEN=0 \
  ./scripts/xcode-archive-upload.sh
```

CI 中 `DISTSRV_SERVER` 和 `DISTSRV_TOKEN` 用 GitHub Secrets / 环境变量提供，不要写进 git。

## 5. 其它 CLI 命令

```bash
distsrv-cli configure --show              # 显示当前配置
distsrv-cli whoami                        # 验证 token

distsrv-cli apps                          # 列出所有应用
distsrv-cli apps --json                   # JSON 输出（方便 jq 处理）
distsrv-cli apps create --short-id newapp --name "New App"

distsrv-cli upload <short_id> <file>
distsrv-cli upload <short_id> <file> --open    # 上传后打开下载页
distsrv-cli upload <short_id> <file> --json    # JSON 输出
```

## 6. 不用 CLI（纯 curl）

CLI 只是个 wrapper。本质就是一个 multipart upload 加 Bearer header：

```bash
TOKEN="dst_xxxxxxxx"
curl -fsSL \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@./MyApp.ipa" \
  https://dist.example.com/api/v1/apps/myapp/upload | jq
```

适合 fastlane / 第三方 CI 集成。

## 7. macOS 快捷指令（Shortcuts.app）

把上传做成右键操作，在 Finder 里右键 .ipa 文件 → "上传到 distsrv"：

1. 打开 **快捷指令** → 新建 → 选 **快速操作**
2. 接收：**文件**（任意类型）
3. 添加 **运行 Shell 脚本**：
   ```bash
   distsrv-cli upload myapp "$1" --open
   ```
4. 保存名为 "上传到 distsrv"
5. Finder 中右键任意 .ipa → 服务 → 上传到 distsrv

## 8. 故障排查

| 现象 | 原因 / 解决 |
|---|---|
| `command not found: distsrv-cli` | 没装或不在 PATH。重跑 `sudo ./install.sh` |
| `HTTP 401: invalid token` | Token 错或被撤销。后台重新生成，`distsrv-cli configure --token ...` |
| Gatekeeper "无法打开" | `xattr -d com.apple.quarantine /usr/local/bin/distsrv-cli`（install.sh 已自动做了） |
| `HTTP 404: app not found` | short_id 写错。`distsrv-cli apps` 看正确 short_id |
| xcodebuild exportArchive 失败 | exportOptions.plist 里 `teamID` 没改，或 Ad Hoc Profile 没包含目标设备 UDID |
