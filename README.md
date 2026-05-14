# distsrv — iOS / Android App 自建分发服务

一个轻量的单二进制 Go 服务，用于在 Ubuntu 服务器上托管 .ipa / .apk 并提供下载页、iOS OTA 安装、UDID 收集、管理后台。

适配硬件：**1 Core / 1 GB / 10 GB**，目标内存占用 < 200 MB。

## 功能

- 一个 URL 自动按 UA 区分 iOS / Android，给出对应安装入口
- iOS OTA 安装（itms-services），自动生成 manifest.plist
- Android APK 直链下载
- UDID 收集（.mobileconfig + 回调端点），方便添加到 Ad Hoc Provisioning Profile
- 管理后台：账号密码登录、上传 .ipa/.apk、自动解析版本与图标、多版本管理、下载统计、应用密码保护
- **多用户 + 角色**：admin 能管用户（创建/禁用/重置密码/角色切换），普通用户只能用应用
- **REST API + API Token**：Mac/CLI/CI 通过 Bearer token 上传，无需账号密码
- HTTPS 自动签发（Let's Encrypt via autocert，零外部依赖）
- 流式上传（200MB 包内存峰值 < 5MB）
- 自动清理旧版本（每平台保留 N 个）
- 暗色模式自动适配

## 部署

### 推荐：一键部署（Ubuntu 22.04 / 24.04）

#### 在开发机：生成发布包

```bash
make release        # 产出 dist/distsrv-<ver>-linux-amd64.tar.gz （和 arm64）
```

每个 tarball 大约 4 MB，自带二进制 + 部署脚本 + 配置示例 + service unit。

#### 在服务器：scp 上传后一条命令完成

```bash
# 在开发机
scp dist/distsrv-*-linux-amd64.tar.gz root@server:/tmp/

# 在服务器
cd /tmp
tar -xzf distsrv-*-linux-amd64.tar.gz -C ~/distsrv-deploy && cd ~/distsrv-deploy

# 交互式（会问域名 / 邮箱，admin 密码自动生成并打印）
sudo ./deploy.sh

# 或者一行无交互
sudo ./deploy.sh --yes \
     --domain dist.example.com \
     --email ops@example.com \
     --org-name "ACME Inc" --org-slug "acme"
```

脚本会自动：
- 安装 `ca-certificates curl sqlite3 iproute2`
- 创建 `distsrv` 系统用户和目录
- 检查 80/443 端口冲突
- 写入 `/etc/distsrv/config.toml`（已有则备份）
- 安装二进制到 `/usr/local/bin/distsrv`
- 安装 systemd unit（内存上限 300M、非 root、安全加固）
- 配置 ufw 防火墙
- 启动服务并做健康检查

部署完成后脚本会打印：访问地址、初始账号、随机生成的 admin 密码、下一步操作。

#### 开发模式（仅 HTTP，:8080，无 HTTPS）

适合在本机或内网测试。iOS OTA 安装在此模式下不可用（Apple 强制 HTTPS）。

```bash
sudo ./deploy.sh --dev --yes
```

#### 常用选项

| 选项 | 作用 |
|---|---|
| `--domain DOMAIN` | 分发域名（生产模式必填） |
| `--email EMAIL` | Let's Encrypt 邮箱 |
| `--admin-pass PASS` | 指定初始密码（留空则随机生成） |
| `--build` | 服务器本地用 `go build` 构建（要预先装 Go） |
| `--binary PATH` | 指定已构建的二进制路径 |
| `--skip-ufw` | 不动防火墙 |
| `--dev` | 开发模式 |
| `--yes` | 非交互 |

完整用法：`./deploy.sh --help`

### DNS

生产模式需要把域名 A 记录指到服务器公网 IP，TTL 300：

```bash
dig +short dist.example.com   # 确认 DNS 已生效
```

DNS 没生效时 Let's Encrypt HTTP-01 挑战会失败，autocert 会反复重试，日志里能看到。

### 部署后操作

1. 浏览器打开 `https://<域名>/admin/login`，用脚本输出的账号密码登录
2. 仪表盘底部"修改我的密码"改密
3. 编辑 `/etc/distsrv/config.toml`，删掉 `[admin].password` 一行
4. `sudo systemctl restart distsrv`
5. 新建应用 `/admin/apps/new`，上传 `.ipa` / `.apk`
6. 分享 `https://<域名>/d/<short_id>` 给测试用户

### 多用户管理

第一个用户（来自 `config.toml` 的 `[admin]`）自动是管理员。登录后在 `/admin/users` 可以：

- **创建用户**：填用户名 + 密码（留空自动生成 16 位强密码，只显示一次）。可选 "管理员" 复选框
- **角色切换**：普通用户 ↔ 管理员
- **禁用/启用**：禁用立即终止该用户的所有 web session 和 API token（401）
- **重置密码**：生成新密码，一次性显示
- **删除**：级联删除该用户的 API token 和 session

权限规则：

| 操作 | 普通用户 | 管理员 |
|---|---|---|
| 查看 / 上传 / 管理所有应用 | ✅ | ✅ |
| 管理自己的 API Token | ✅ | ✅ |
| 看其他用户的 API Token | ❌ | ❌（每人只看自己的） |
| `/admin/users` 用户管理 | ❌ 403 | ✅ |
| 撤销自己的 admin / 删除自己 | — | ❌（防自伤） |
| 撤销/禁用最后一个 admin | — | ❌（系统至少保留一个 admin） |

升级提醒：从 pre-multiuser 版本升级时，db migration 会把最老的 user 自动标为 admin（保证升级后还能登录）。

### Mac 端快捷发布（distsrv-cli）

让 iOS 开发者在 Mac 上 **一条命令** 把 ipa 发到 distsrv，或在 Xcode Archive 后自动 export & upload：

```bash
# 在管理机：构建 Mac CLI tarball
make release-mac
# → dist/distsrv-cli-<ver>-darwin-arm64.tar.gz （M1/M2/M3）
# → dist/distsrv-cli-<ver>-darwin-amd64.tar.gz （Intel）

# 在 Mac 上：
tar -xzf distsrv-cli-*-darwin-arm64.tar.gz
cd distsrv-cli-*
sudo ./install.sh
distsrv-cli configure --server https://dist.example.com --token <token>
distsrv-cli upload myapp ./build/MyApp.ipa --open
```

token 在 `/admin/tokens` 创建。Xcode Archive 自动上传、CI 集成、Finder 右键快捷指令的完整说明在 [mac/README.md](mac/README.md)。

REST API（不需要 CLI 也能集成 fastlane / 自建 CI）：

```bash
curl -fsSL -H "Authorization: Bearer <token>" \
     -F "file=@./MyApp.ipa" \
     https://dist.example.com/api/v1/apps/myapp/upload
```

API 路由：`GET /api/v1/apps`、`POST /api/v1/apps`、`GET /api/v1/apps/{short_id}`、`POST /api/v1/apps/{short_id}/upload`、`GET /api/v1/whoami`、`GET /api/v1/server`（公开）。

### GitHub Release 自动发布

把代码推到 GitHub 后，每次发版只需 push tag 就自动构建并发布全平台二进制：

```bash
git tag v0.1.0
git push --tags
# GitHub Actions 触发 → matrix 构建 → 创建 Release → 上传 7 个 tarball + SHA256SUMS.txt
```

包含的工作流：

| 文件 | 触发 | 做什么 |
|---|---|---|
| [.github/workflows/release.yml](.github/workflows/release.yml) | push tag `v*` 或手动 | `make release-all` 产出 7 个 tarball + 校验和，`softprops/action-gh-release` 创建 Release |
| [.github/workflows/ci.yml](.github/workflows/ci.yml) | push/PR | `go vet`、`go test -race`、5 平台 cross-compile 烟雾测试 + `docker-test.sh` 端到端 |

#### 不想 push tag？本地 `gh` 一键发布

需要 [GitHub CLI](https://cli.github.com/) 且已 `gh auth login`：

```bash
./scripts/release-local.sh v0.1.0                # 正式发布
./scripts/release-local.sh v0.1.0 --draft        # 草稿
./scripts/release-local.sh v0.1.0 --prerelease   # 预发布
./scripts/release-local.sh v0.1.0 --no-tag       # tag 已存在，跳过 git tag 步骤
```

脚本会校验工作树干净 → `go vet` + `go test` → `make release-all` → `git tag` + push → `gh release create` 上传产物 + 自动生成 changelog（从上一个 tag 到本 tag 的 commit）。

#### Mac CLI 在线安装（用 GitHub Release）

Release 发布后，Mac 用户可以一行装：

```bash
export DISTSRV_REPO=gregrgr/distsrv
curl -fsSL https://raw.githubusercontent.com/$DISTSRV_REPO/main/mac/install-remote.sh | bash
```

脚本会自动检测 arm64/amd64，从 latest release 拉对应 tarball，校验 SHA256，剥 Gatekeeper quarantine，装到 `/usr/local/bin/`。指定版本用 `DISTSRV_TAG=v0.1.0`。

#### 本地构建所有 release artifacts

不发布也可以单纯打包：

```bash
make release-all VERSION=v0.1.0
ls dist/
# distsrv-v0.1.0-linux-amd64.tar.gz           (server)
# distsrv-v0.1.0-linux-arm64.tar.gz           (server)
# distsrv-cli-v0.1.0-darwin-arm64.tar.gz      (cli + mac/* tools)
# distsrv-cli-v0.1.0-darwin-amd64.tar.gz      (cli + mac/* tools)
# distsrv-cli-v0.1.0-linux-amd64.tar.gz       (cli only)
# distsrv-cli-v0.1.0-linux-arm64.tar.gz       (cli only)
# distsrv-cli-v0.1.0-windows-amd64.tar.gz     (cli .exe only)
# SHA256SUMS.txt
```

### iOS UDID 自动收集（可选：装 Apple code-signing 证书）

iOS 16+/26 拒绝用 Let's Encrypt TLS 证书签名的 `.mobileconfig`。要让 **"一键自动收集 UDID"** 功能工作，需要给 distsrv 喂一份 Apple Developer 颁发的 code-signing 证书。

**没装这个证书也能用**：下载页会自动切换到"手动提交 UDID"模式 — 用户在 iPhone 设置里 `通用 → 关于本机 → 长按序列号 → 复制 UDID`，回到 Safari 表单粘贴提交。所有 iOS 版本都可用，剪贴板自动粘贴让操作变成 3 步。

**装了证书后**：下载页会显示"📥 一键获取 UDID（自动）"按钮，用户点一下安装 profile，iPhone 把 UDID 直接 POST 回 distsrv 写入设备列表。

#### 配置

把 Apple Developer Portal 导出的 `.p12` scp 到 server，在 `/etc/distsrv/config.toml` 里：

```toml
[server.profile_signing]
pkcs12_file = "/etc/distsrv/profile-signing.p12"
pkcs12_password = ""     # 留空 + 用环境变量 DISTSRV_P12_PASSWORD 覆盖更安全
```

或者用 PEM 形式：

```toml
[server.profile_signing]
cert_file = "/etc/distsrv/profile-signing.crt"   # 可含 chain
key_file  = "/etc/distsrv/profile-signing.key"
```

`sudo systemctl restart distsrv` 后日志里会看到 `loaded profile-signing cert: subject="..." issuer="..." expires=...`。

### Docker 模拟部署（验证脚本）

如果想在部署到真实服务器前先试一遍 `deploy.sh`：

```bash
./docker-test.sh           # 起 ubuntu:24.04 容器，跑一遍完整流程，最后自动清理
./docker-test.sh --keep    # 测试完保留容器，方便手动 docker exec 进去看
```

脚本会：构建 Linux amd64 二进制 → 启动 `ubuntu:24.04` → `docker cp` 项目进去 → 跑 `deploy.sh --dev --no-systemd --skip-ufw` → 在容器内做 `/healthz` / 登录 / 新建 app / 下载页 / 静态资源端到端验证 → 检查文件权限和日志 → 清理。整个流程约 1-2 分钟（首次拉镜像更久）。

容器内不能用 systemd，所以脚本走 `--no-systemd` 模式（nohup + setpriv 启动），这只是测试方便。**真正部署到 Ubuntu 服务器时去掉这个标志，让 systemd 接管。**

### 手动部署（备选）

如果不想用脚本：参考 [distsrv.service](distsrv.service) 和 [config.example.toml](config.example.toml)，按照脚本做的事情手动执行即可。

### iOS Ad Hoc UDID 流程

1. iPhone 用 Safari 打开 `https://<域名>/d/<short_id>`
2. 点击"获取本机 UDID" → 安装描述文件 → 设置 → 通用 → VPN 与设备管理 → 批准
3. 后台 `/admin/apps/<id>/udids` 看到 UDID（可导出 CSV）
4. 把 UDID 加进 Apple Developer Portal → Devices
5. 重新生成 Provisioning Profile，重打 ipa
6. 后台上传新的 ipa，用户重新打开下载页直接 OTA 安装

## 本地开发

```bash
make build
./distsrv -config ./config.dev.toml
# 打开 http://localhost:8080
```

dev_mode 下走明文 HTTP，**iOS OTA 不可用**（Apple 强制 HTTPS），但可以测后台和 APK 下载。

## 备份

数据目录 `/var/lib/distsrv` 包含数据库和上传文件。SQLite 不能直接拷贝 `.db-wal`，必须用 `.backup`：

```bash
# /etc/cron.daily/distsrv-backup
sudo mkdir -p /var/backups/distsrv
sudo sqlite3 /var/lib/distsrv/distsrv.db ".backup '/var/backups/distsrv/distsrv-$(date +%F).db'"
# 上传文件用 rsync 推到对象存储（强烈建议，10GB 磁盘没有冗余）
```

## 常见 iOS 安装问题

| 现象 | 原因 | 解决 |
|---|---|---|
| 点"安装"无反应 | 不是 Safari，或者 itms-services URL 错 | 用 Safari；查 manifest URL |
| "无法连接 dist.example.com" | HTTPS 证书未签发 / 域名 DNS 未生效 | `journalctl -u distsrv` 看 autocert，`dig` 看 DNS |
| "无法安装 [App]" | 设备 UDID 未在 Provisioning Profile 中 | 走"获取本机 UDID" → 加进开发者后台 → 重新打包 |
| 进度条到一半失败 | 网络中断 / 服务被 reload | 重试；检查 systemctl status distsrv |
| 安装完打不开"未受信任的开发者" | 未在设置中信任开发者 | 设置 → 通用 → VPN 与设备管理 |

## 架构速览

- 单进程 Go，直接监听 80/443（无 nginx/caddy 反代）
- 路由：[github.com/go-chi/chi/v5](https://github.com/go-chi/chi)
- SQLite：[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)（纯 Go，无 CGO）
- IPA 解析：`archive/zip` + [howett.net/plist](https://pkg.go.dev/howett.net/plist)
- APK 解析：[shogo82148/androidbinary](https://github.com/shogo82148/androidbinary)
- HTTPS 自动化：`golang.org/x/crypto/acme/autocert`
- 模板：标准库 `html/template`，资源通过 `embed` 编译进二进制

## 许可

MIT
