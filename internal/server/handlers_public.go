package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skip2/go-qrcode"
	"go.mozilla.org/pkcs7"
	"howett.net/plist"

	"distsrv/internal/auth"
	"distsrv/internal/db"
	"distsrv/internal/parser"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.renderHTML(w, http.StatusOK, "index.html", map[string]any{
		"Site": s.cfg.Site,
	})
}

type downloadPageData struct {
	App             *db.App
	IOSVersion      *db.Version
	AndroidVersion  *db.Version
	PlatformHint    string // ios | android | both
	// ITMSURL and QRDataURL must be template.URL so html/template lets
	// the non-standard itms-services:// and data:image/... schemes
	// through (otherwise they get rewritten to "#ZgotmplZ").
	ITMSURL         template.URL
	APKURL          string
	AppIconURL      string
	MobileconfigURL string
	QRDataURL       template.URL
	Site            any
	NeedsPassword   bool
	PasswordError   string
	BaseURL         string
}

func (s *Server) handleDownloadPage(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	app, err := s.db.GetAppByShortID(shortID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Password gate
	if app.PasswordHash != "" && !s.hasAppPasswordCookie(r, app.ID) {
		s.renderHTML(w, http.StatusOK, "download_password.html", map[string]any{
			"App":  app,
			"Site": s.cfg.Site,
		})
		return
	}

	data := downloadPageData{
		App:     app,
		Site:    s.cfg.Site,
		BaseURL: s.baseURL(),
	}

	if app.CurrentIOSVersionID.Valid {
		if v, err := s.db.GetVersion(app.CurrentIOSVersionID.Int64); err == nil {
			data.IOSVersion = v
			manifestURL := fmt.Sprintf("%s/manifest/%d.plist", s.baseURL(), v.ID)
			data.ITMSURL = template.URL("itms-services://?action=download-manifest&url=" + manifestURL)
			if v.IconPath != "" && data.AppIconURL == "" {
				data.AppIconURL = fmt.Sprintf("%s/icon/%d.png", s.baseURL(), v.ID)
			}
		}
	}
	if app.CurrentAndroidVersionID.Valid {
		if v, err := s.db.GetVersion(app.CurrentAndroidVersionID.Int64); err == nil {
			data.AndroidVersion = v
			data.APKURL = fmt.Sprintf("%s/file/%d/%s", s.baseURL(), v.ID, filepath.Base(v.FilePath))
			if v.IconPath != "" && data.AppIconURL == "" {
				data.AppIconURL = fmt.Sprintf("%s/icon/%d.png", s.baseURL(), v.ID)
			}
		}
	}
	data.MobileconfigURL = fmt.Sprintf("%s/mobileconfig/%s.mobileconfig", s.baseURL(), shortID)

	switch {
	case data.IOSVersion != nil && data.AndroidVersion != nil:
		data.PlatformHint = uaPlatform(r)
	case data.IOSVersion != nil:
		data.PlatformHint = "ios"
	case data.AndroidVersion != nil:
		data.PlatformHint = "android"
	}

	// Embed QR as data URL
	pageURL := fmt.Sprintf("%s/d/%s", s.baseURL(), shortID)
	if png, err := qrcode.Encode(pageURL, qrcode.Medium, 256); err == nil {
		data.QRDataURL = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
	}

	s.renderHTML(w, http.StatusOK, "download.html", data)
}

func (s *Server) handleDownloadAuth(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	app, err := s.db.GetAppByShortID(shortID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if app.PasswordHash == "" {
		http.Redirect(w, r, "/d/"+shortID, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	if !auth.VerifyPassword(app.PasswordHash, pw) {
		s.renderHTML(w, http.StatusOK, "download_password.html", map[string]any{
			"App":     app,
			"Site":    s.cfg.Site,
			"Error":   "密码错误",
		})
		return
	}
	cookie := &http.Cookie{
		Name:     appPasswordCookieName(app.ID),
		Value:    appPasswordCookieValue(app),
		Path:     "/d/" + shortID,
		MaxAge:   60 * 60 * 24, // 24h
		HttpOnly: true,
		Secure:   !s.cfg.Server.DevMode,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/d/"+shortID, http.StatusSeeOther)
}

func (s *Server) handleDownloadQR(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	if _, err := s.db.GetAppByShortID(shortID); err != nil {
		http.NotFound(w, r)
		return
	}
	pageURL := fmt.Sprintf("%s/d/%s", s.baseURL(), shortID)
	png, err := qrcode.Encode(pageURL, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "qr error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(png)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	vid, err := strconv.ParseInt(chi.URLParam(r, "vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.db.GetVersion(vid)
	if err != nil || v.Platform != "ios" {
		http.NotFound(w, r)
		return
	}
	app, err := s.db.GetApp(v.AppID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if app.PasswordHash != "" && !s.hasAppPasswordCookie(r, app.ID) {
		http.Error(w, "password required", http.StatusUnauthorized)
		return
	}

	ipaURL := fmt.Sprintf("%s/file/%d/%s", s.baseURL(), v.ID, filepath.Base(v.FilePath))
	iconURL := ""
	if v.IconPath != "" {
		iconURL = fmt.Sprintf("%s/icon/%d.png", s.baseURL(), v.ID)
	}
	title := app.Name
	if title == "" {
		title = v.BundleID
	}
	bundleVer := v.VersionCode
	if bundleVer == "" {
		bundleVer = v.VersionName
	}

	data := map[string]any{
		"IPAUrl":        ipaURL,
		"IconUrl":       iconURL,
		"BundleID":      v.BundleID,
		"BundleVersion": bundleVer,
		"Title":         title,
	}
	s.renderPlist(w, "manifest.plist.tmpl", data, "application/xml; charset=utf-8")
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	vid, err := strconv.ParseInt(chi.URLParam(r, "vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.db.GetVersion(vid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	app, err := s.db.GetApp(v.AppID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if app.PasswordHash != "" && !s.hasAppPasswordCookie(r, app.ID) {
		http.Error(w, "password required", http.StatusUnauthorized)
		return
	}

	absPath := s.storage.AbsPath(v.FilePath)
	f, err := os.Open(absPath)
	if err != nil {
		http.Error(w, "file missing on disk", http.StatusGone)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat error", http.StatusInternalServerError)
		return
	}

	// Force iOS to honor itms-services: the .ipa must be served as octet-stream.
	ext := strings.ToLower(filepath.Ext(v.FilePath))
	switch ext {
	case ".ipa":
		w.Header().Set("Content-Type", "application/octet-stream")
	case ".apk":
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(v.FilePath)+`"`)

	_ = s.db.RecordDownload(v.AppID, v.ID, v.Platform, clientIP(r), r.UserAgent(), "")

	http.ServeContent(w, r, filepath.Base(v.FilePath), stat.ModTime(), f)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	vid, err := strconv.ParseInt(chi.URLParam(r, "vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.db.GetVersion(vid)
	if err != nil || v.IconPath == "" {
		http.NotFound(w, r)
		return
	}
	abs := s.storage.AbsPath(v.IconPath)

	// Lazy migration: icons uploaded before the CgBI fix are still
	// Apple-optimized PNGs that don't render outside Safari/UIKit.
	// Detect + rewrite once, persistently.
	if data, err := os.ReadFile(abs); err == nil && parser.IsAppleCgBIPNG(data) {
		fixed := parser.NormalizeAppleCgBI(data)
		if !bytes.Equal(fixed, data) {
			_ = os.WriteFile(abs, fixed, 0o640)
		}
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, abs)
}

func (s *Server) handleMobileconfig(w http.ResponseWriter, r *http.Request) {
	shortID := chi.URLParam(r, "shortID")
	app, err := s.db.GetAppByShortID(shortID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// iOS requires PayloadUUID to be a real RFC 4122 UUID v4 (with dashes).
	// A plain hex token here makes the device reject the profile as
	// "无效的描述文件" / "Invalid profile".
	uuid, err := auth.RandomUUIDv4()
	if err != nil {
		http.Error(w, "uuid error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Host":        hostOnly(s.cfg.Server.Domain, s.cfg.Server.DevAddr, s.cfg.Server.DevMode),
		"AppShortID":  shortID,
		"AppName":     app.Name,
		"OrgName":     s.cfg.Site.OrgName,
		"OrgSlug":     s.cfg.Site.OrgSlug,
		"PayloadUUID": uuid,
	}
	w.Header().Set("Content-Type", "application/x-apple-aspen-config; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="udid.mobileconfig"`)
	s.renderPlist(w, "mobileconfig.tmpl", data, "application/x-apple-aspen-config; charset=utf-8")
}

func (s *Server) handleUDIDCallback(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	shortID := r.URL.Query().Get("app")

	// Body is a PKCS7-signed plist; try to parse signature first, fall back to
	// scanning the raw body for the <plist> tag if PKCS7 parsing fails.
	var plistData []byte
	if p7, err := pkcs7.Parse(body); err == nil && len(p7.Content) > 0 {
		plistData = p7.Content
	} else {
		plistData = extractPlistFragment(body)
	}
	if plistData == nil {
		http.Error(w, "no plist payload", http.StatusBadRequest)
		return
	}

	var payload struct {
		UDID    string `plist:"UDID"`
		IMEI    string `plist:"IMEI"`
		Serial  string `plist:"SERIAL"`
		Version string `plist:"VERSION"`
		Product string `plist:"PRODUCT"`
	}
	if _, err := plist.Unmarshal(plistData, &payload); err != nil {
		http.Error(w, "plist decode error", http.StatusBadRequest)
		return
	}

	if shortID != "" && payload.UDID != "" {
		if app, err := s.db.GetAppByShortID(shortID); err == nil {
			_ = s.db.UpsertUDID(&db.UDID{
				AppID:   app.ID,
				UDID:    payload.UDID,
				Product: payload.Product,
				Version: payload.Version,
				Serial:  payload.Serial,
				IMEI:    payload.IMEI,
			})
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "UDID 已记录：%s\n请联系管理员将该设备添加到 Provisioning Profile。", payload.UDID)
}

// ---- helpers ----

func uaPlatform(r *http.Request) string {
	ua := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ipod"):
		return "ios"
	case strings.Contains(ua, "android"):
		return "android"
	}
	return "both"
}

func hostOnly(domain, devAddr string, devMode bool) string {
	if devMode {
		return "localhost" + devAddr
	}
	return domain
}

func appPasswordCookieName(appID int64) string {
	return fmt.Sprintf("distsrv_app_%d", appID)
}

func appPasswordCookieValue(app *db.App) string {
	sum := sha256.Sum256([]byte(app.PasswordHash))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) hasAppPasswordCookie(r *http.Request, appID int64) bool {
	app, err := s.db.GetApp(appID)
	if err != nil {
		return false
	}
	c, err := r.Cookie(appPasswordCookieName(appID))
	if err != nil {
		return false
	}
	return c.Value == appPasswordCookieValue(app)
}

// extractPlistFragment finds a <plist ...>...</plist> block inside arbitrary bytes.
// Used as a fallback when PKCS7 signature parsing fails.
func extractPlistFragment(b []byte) []byte {
	start := bytes.Index(b, []byte("<plist"))
	if start < 0 {
		return nil
	}
	endTag := []byte("</plist>")
	endIdx := bytes.Index(b[start:], endTag)
	if endIdx < 0 {
		return nil
	}
	return b[start : start+endIdx+len(endTag)]
}
