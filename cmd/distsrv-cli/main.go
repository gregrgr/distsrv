// distsrv-cli — command-line client for the distsrv app distribution service.
// Designed for macOS (Xcode integration), but cross-platform.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const userAgent = "distsrv-cli/0.1"

type Config struct {
	Server string `toml:"server"`
	Token  string `toml:"token"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	var err error
	switch sub {
	case "configure":
		err = cmdConfigure(args)
	case "whoami":
		err = cmdWhoami(args)
	case "apps":
		err = cmdApps(args)
	case "upload":
		err = cmdUpload(args)
	case "version", "-v", "--version":
		fmt.Println("distsrv-cli 0.1")
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `distsrv-cli — distsrv 客户端

用法：
  distsrv-cli <command> [options]

命令：
  configure   设置服务器地址和 API token
  whoami      查看当前身份（验证 token）
  apps        列出 / 创建应用
  upload      上传 .ipa / .apk 到指定应用
  version     版本信息
  help        本帮助

示例：
  distsrv-cli configure --server https://dist.example.com --token dst_xxxxxxxx
  distsrv-cli apps
  distsrv-cli apps create --short-id myapp --name "My App"
  distsrv-cli upload myapp ./build/MyApp.ipa
  distsrv-cli upload myapp ./build/MyApp.ipa --open

配置文件位置：
  macOS/Linux: ~/.config/distsrv-cli/config.toml
  Windows:     %APPDATA%\distsrv-cli\config.toml
`)
}

// ============================================================
// configure
// ============================================================

func cmdConfigure(args []string) error {
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	server := fs.String("server", "", "distsrv server base URL (例如 https://dist.example.com)")
	token := fs.String("token", "", "API token (从 /admin/tokens 获取)")
	show := fs.Bool("show", false, "显示当前配置")
	_ = fs.Parse(args)

	if *show {
		cfg, _ := loadConfig()
		fmt.Println("server:", cfg.Server)
		if cfg.Token == "" {
			fmt.Println("token: (未设置)")
		} else {
			fmt.Println("token:", maskToken(cfg.Token))
		}
		fmt.Println("config:", configPath())
		return nil
	}

	cfg, _ := loadConfig()
	if *server != "" {
		cfg.Server = strings.TrimRight(*server, "/")
	}
	if *token != "" {
		cfg.Token = *token
	}
	if cfg.Server == "" {
		return errors.New("--server 必填（或先 configure 过）")
	}
	if cfg.Token == "" {
		return errors.New("--token 必填（或先 configure 过）。在 /admin/tokens 创建")
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("已写入 %s\n  server: %s\n  token:  %s\n", configPath(), cfg.Server, maskToken(cfg.Token))
	return nil
}

func maskToken(t string) string {
	if len(t) <= 10 {
		return strings.Repeat("*", len(t))
	}
	return t[:10] + "…" + strings.Repeat("*", 6)
}

// ============================================================
// whoami
// ============================================================

func cmdWhoami(args []string) error {
	cfg, err := loadConfigStrict()
	if err != nil {
		return err
	}
	var out struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := apiGet(cfg, "/api/v1/whoami", &out); err != nil {
		return err
	}
	fmt.Printf("user_id: %d\nusername: %s\nserver: %s\n", out.ID, out.Username, cfg.Server)
	return nil
}

// ============================================================
// apps
// ============================================================

type appView struct {
	ID           int64    `json:"id"`
	ShortID      string   `json:"short_id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	IOSBundleID  string   `json:"ios_bundle_id,omitempty"`
	AndroidPkg   string   `json:"android_package,omitempty"`
	IOSCurrent   *verView `json:"ios_current,omitempty"`
	AndroidVer   *verView `json:"android_current,omitempty"`
	DownloadPage string   `json:"download_page_url"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

type verView struct {
	ID          int64  `json:"id"`
	Platform    string `json:"platform"`
	VersionName string `json:"version_name"`
	VersionCode string `json:"version_code"`
	BundleID    string `json:"bundle_id"`
	FileSize    int64  `json:"file_size"`
	FileSHA256  string `json:"file_sha256"`
	UploadedAt  int64  `json:"uploaded_at"`
	DownloadURL string `json:"download_url"`
}

func cmdApps(args []string) error {
	if len(args) > 0 && args[0] == "create" {
		return cmdAppsCreate(args[1:])
	}
	fs := flag.NewFlagSet("apps", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "输出原始 JSON")
	_ = fs.Parse(args)

	cfg, err := loadConfigStrict()
	if err != nil {
		return err
	}
	var resp struct {
		Apps []appView `json:"apps"`
	}
	if err := apiGet(cfg, "/api/v1/apps", &resp); err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(resp.Apps) == 0 {
		fmt.Println("(没有应用，用 'distsrv-cli apps create' 创建)")
		return nil
	}
	fmt.Printf("%-16s %-24s %-16s %-16s %s\n", "SHORT_ID", "NAME", "IOS", "ANDROID", "URL")
	for _, a := range resp.Apps {
		ios := "—"
		if a.IOSCurrent != nil {
			ios = fmt.Sprintf("%s(%s)", a.IOSCurrent.VersionName, a.IOSCurrent.VersionCode)
		}
		and := "—"
		if a.AndroidVer != nil {
			and = fmt.Sprintf("%s(%s)", a.AndroidVer.VersionName, a.AndroidVer.VersionCode)
		}
		name := a.Name
		if len(name) > 24 {
			name = name[:21] + "..."
		}
		fmt.Printf("%-16s %-24s %-16s %-16s %s\n", a.ShortID, name, ios, and, a.DownloadPage)
	}
	return nil
}

func cmdAppsCreate(args []string) error {
	fs := flag.NewFlagSet("apps create", flag.ExitOnError)
	shortID := fs.String("short-id", "", "URL short id (字母数字/-/_，2-32)")
	name := fs.String("name", "", "应用名")
	desc := fs.String("description", "", "描述（可选）")
	_ = fs.Parse(args)
	if *shortID == "" || *name == "" {
		return errors.New("--short-id 和 --name 必填")
	}
	cfg, err := loadConfigStrict()
	if err != nil {
		return err
	}
	body := map[string]string{
		"short_id":    *shortID,
		"name":        *name,
		"description": *desc,
	}
	var out appView
	if err := apiPostJSON(cfg, "/api/v1/apps", body, &out); err != nil {
		return err
	}
	fmt.Printf("已创建应用：%s\n  ID: %d\n  下载页：%s\n", out.ShortID, out.ID, out.DownloadPage)
	return nil
}

// ============================================================
// upload
// ============================================================

func cmdUpload(args []string) error {
	// Hand-parsed so flags can appear in any position
	// (the std flag pkg stops parsing at the first positional arg).
	openBrowser := false
	asJSON := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--open", "-o":
			openBrowser = true
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Println("用法：distsrv-cli upload <short_id> <file.ipa|file.apk> [--open] [--json]")
			return nil
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag: %s", a)
			}
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return errors.New("用法：distsrv-cli upload <short_id> <file.ipa|file.apk> [--open] [--json]")
	}
	shortID := positional[0]
	filePath := positional[1]
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".ipa" && ext != ".apk" {
		return errors.New("文件后缀必须是 .ipa 或 .apk")
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("读取文件：%w", err)
	}
	cfg, err := loadConfigStrict()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "上传 %s (%.1f MB) → %s/%s ...\n",
		filepath.Base(filePath), float64(fi.Size())/1024/1024, cfg.Server, shortID)

	endpoint := cfg.Server + "/api/v1/apps/" + url.PathEscape(shortID) + "/upload"

	var out struct {
		Version      verView `json:"version"`
		DownloadPage string  `json:"download_page"`
	}
	if err := apiUploadFile(cfg, endpoint, filePath, &out); err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Fprintln(os.Stderr, "✓ 上传成功")
	fmt.Printf("版本：%s (%s)\n", out.Version.VersionName, out.Version.VersionCode)
	fmt.Printf("大小：%.1f MB\n", float64(out.Version.FileSize)/1024/1024)
	fmt.Printf("下载页：%s\n", out.DownloadPage)
	fmt.Printf("文件 URL：%s\n", out.Version.DownloadURL)

	if openBrowser {
		_ = openURL(out.DownloadPage)
	}
	return nil
}

// ============================================================
// Config I/O
// ============================================================

func configPath() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "distsrv-cli", "config.toml")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "distsrv-cli", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "distsrv-cli", "config.toml")
}

func loadConfig() (*Config, error) {
	p := configPath()
	var c Config
	if _, err := toml.DecodeFile(p, &c); err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			return &c, nil
		}
		return &c, err
	}
	return &c, nil
}

func loadConfigStrict() (*Config, error) {
	c, _ := loadConfig()
	// Allow env overrides for CI use
	if v := os.Getenv("DISTSRV_SERVER"); v != "" {
		c.Server = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("DISTSRV_TOKEN"); v != "" {
		c.Token = v
	}
	if c.Server == "" {
		return nil, errors.New("未配置 server。先跑：distsrv-cli configure --server URL --token TOKEN\n(也可用环境变量 DISTSRV_SERVER / DISTSRV_TOKEN)")
	}
	if c.Token == "" {
		return nil, errors.New("未配置 token。先跑：distsrv-cli configure --token TOKEN\n(也可用环境变量 DISTSRV_TOKEN)")
	}
	return c, nil
}

func saveConfig(c *Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o600)
}

// ============================================================
// HTTP helpers
// ============================================================

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Minute}
}

func apiGet(cfg *Config, path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, cfg.Server+path, nil)
	if err != nil {
		return err
	}
	return doRequest(cfg, req, out)
}

func apiPostJSON(cfg *Config, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Server+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doRequest(cfg, req, out)
}

func doRequest(cfg *Config, req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// apiUploadFile streams a file as multipart/form-data without buffering into memory.
func apiUploadFile(cfg *Config, endpoint, filePath string, out any) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		var werr error
		defer func() { _ = pw.CloseWithError(werr) }()
		part, err := mw.CreateFormFile("file", filepath.Base(filePath))
		if err != nil {
			werr = err
			return
		}
		// Stream copy with a small buffer; show a coarse progress bar to stderr.
		buf := make([]byte, 256*1024)
		var sent int64
		lastPrint := time.Now().Add(-time.Hour)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				if _, werr = part.Write(buf[:n]); werr != nil {
					return
				}
				sent += int64(n)
				if time.Since(lastPrint) > 250*time.Millisecond || sent == fi.Size() {
					printProgress(sent, fi.Size())
					lastPrint = time.Now()
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				werr = rerr
				return
			}
		}
		werr = mw.Close()
	}()

	req, err := http.NewRequest(http.MethodPost, endpoint, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("User-Agent", userAgent)
	req.ContentLength = -1 // unknown (streaming)

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	fmt.Fprintln(os.Stderr) // newline after progress bar

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func printProgress(sent, total int64) {
	const w = 30
	frac := float64(sent) / float64(total)
	if frac > 1 {
		frac = 1
	}
	full := int(frac * float64(w))
	bar := strings.Repeat("█", full) + strings.Repeat("·", w-full)
	fmt.Fprintf(os.Stderr, "\r  [%s] %5.1f%%  %.1f/%.1f MB", bar, frac*100,
		float64(sent)/1024/1024, float64(total)/1024/1024)
}

// openURL opens the given URL in the default browser.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
