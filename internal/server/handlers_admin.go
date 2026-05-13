package server

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"distsrv/internal/auth"
	"distsrv/internal/db"
)

var shortIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{2,32}$`)

// ---- Auth ----

func (s *Server) handleAdminLoginGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); ok {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	s.renderHTML(w, http.StatusOK, "admin_login.html", map[string]any{
		"Site": s.cfg.Site,
		"Next": r.URL.Query().Get("next"),
	})
}

func (s *Server) handleAdminLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	next := r.FormValue("next")

	u, err := s.db.GetUserByUsername(username)
	if err != nil || !auth.VerifyPassword(u.PasswordHash, password) {
		s.renderHTML(w, http.StatusUnauthorized, "admin_login.html", map[string]any{
			"Site":  s.cfg.Site,
			"Error": "用户名或密码错误",
			"Next":  next,
		})
		return
	}
	if u.Disabled() {
		s.renderHTML(w, http.StatusUnauthorized, "admin_login.html", map[string]any{
			"Site":  s.cfg.Site,
			"Error": "账号已被禁用，请联系管理员",
			"Next":  next,
		})
		return
	}
	if err := s.createSession(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	_ = s.db.TouchUserLogin(u.ID)
	if next == "" || !strings.HasPrefix(next, "/admin") {
		next = "/admin/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	s.destroySession(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	old := r.FormValue("old_password")
	newPw := r.FormValue("new_password")
	if !auth.VerifyPassword(u.PasswordHash, old) {
		http.Error(w, "原密码错误", http.StatusBadRequest)
		return
	}
	if len(newPw) < 8 {
		http.Error(w, "新密码至少 8 位", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(newPw, s.cfg.Security.BcryptCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateUserPassword(u.ID, hash); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// ---- Dashboard ----

type dashboardAppRow struct {
	App           *db.App
	IOSVersion    *db.Version
	AndroidVer    *db.Version
	IOSDownloads  int
	AndroidDownls int
}

func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	apps, err := s.db.ListApps()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rows []dashboardAppRow
	for _, a := range apps {
		row := dashboardAppRow{App: a}
		if a.CurrentIOSVersionID.Valid {
			if v, err := s.db.GetVersion(a.CurrentIOSVersionID.Int64); err == nil {
				row.IOSVersion = v
				n, _ := s.db.CountDownloadsByVersion(v.ID)
				row.IOSDownloads = n
			}
		}
		if a.CurrentAndroidVersionID.Valid {
			if v, err := s.db.GetVersion(a.CurrentAndroidVersionID.Int64); err == nil {
				row.AndroidVer = v
				n, _ := s.db.CountDownloadsByVersion(v.ID)
				row.AndroidDownls = n
			}
		}
		rows = append(rows, row)
	}

	freeBytes := s.storage.FreeBytes()
	s.renderHTML(w, http.StatusOK, "admin_dashboard.html", map[string]any{
		"User":      u,
		"Site":      s.cfg.Site,
		"Apps":      rows,
		"FreeBytes": freeBytes,
		"BaseURL":   s.baseURL(),
	})
}

// ---- App CRUD ----

func (s *Server) handleAdminAppNewGet(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	s.renderHTML(w, http.StatusOK, "admin_app_new.html", map[string]any{
		"User": u, "Site": s.cfg.Site,
	})
}

func (s *Server) handleAdminAppNewPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	shortID := strings.TrimSpace(r.FormValue("short_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	if !shortIDRe.MatchString(shortID) {
		http.Error(w, "short_id 只能包含字母数字、下划线、短横线，长度 2-32", http.StatusBadRequest)
		return
	}
	if name == "" {
		http.Error(w, "name 不能为空", http.StatusBadRequest)
		return
	}
	id, err := s.db.CreateApp(shortID, name, description)
	if err != nil {
		http.Error(w, "创建失败："+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", id), http.StatusSeeOther)
}

func (s *Server) handleAdminAppDetail(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	app, err := s.db.GetApp(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	iosVersions, _ := s.db.ListVersions(id, "ios")
	androidVersions, _ := s.db.ListVersions(id, "android")
	udids, _ := s.db.ListUDIDs(id)

	s.renderHTML(w, http.StatusOK, "admin_app_detail.html", map[string]any{
		"User":            u,
		"Site":            s.cfg.Site,
		"App":             app,
		"IOSVersions":     iosVersions,
		"AndroidVersions": androidVersions,
		"UDIDCount":       len(udids),
		"BaseURL":         s.baseURL(),
	})
}

func (s *Server) handleAdminAppEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	shortID := strings.TrimSpace(r.FormValue("short_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	if !shortIDRe.MatchString(shortID) {
		http.Error(w, "short_id 格式错误", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateApp(id, name, description, shortID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", id), http.StatusSeeOther)
}

func (s *Server) handleAdminAppSetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	hash := ""
	if pw != "" {
		h, err := auth.HashPassword(pw, s.cfg.Security.BcryptCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hash = h
	}
	if err := s.db.SetAppPassword(id, hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", id), http.StatusSeeOther)
}

func (s *Server) handleAdminAppDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	// Best-effort delete of files.
	versions, _ := s.db.ListVersions(id, "")
	for _, v := range versions {
		_ = s.storage.DeleteVersionFiles(v)
	}
	if err := s.db.DeleteApp(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleAdminAppUpload(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.db.GetApp(id); err != nil {
		http.NotFound(w, r)
		return
	}

	maxBytes := int64(s.cfg.Storage.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "需要 multipart/form-data 上传", http.StatusBadRequest)
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "读取上传失败："+err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		res, err := s.storage.SaveUpload(part, id, u.ID)
		_ = part.Close()
		if err != nil {
			http.Error(w, "上传失败："+err.Error(), http.StatusBadRequest)
			return
		}
		_ = res
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", id), http.StatusSeeOther)
}

// ---- Versions ----

func (s *Server) handleAdminVersionSetCurrent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.db.GetVersion(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.SetAppCurrentVersion(v.AppID, v.ID, v.Platform); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", v.AppID), http.StatusSeeOther)
}

func (s *Server) handleAdminVersionDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.db.GetVersion(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	app, err := s.db.GetApp(v.AppID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// If this is the current version, clear the pointer first (DB has ON DELETE SET NULL semantics
	// for users but apps.current_* are plain columns, so handle here).
	if v.Platform == "ios" && app.CurrentIOSVersionID.Valid && app.CurrentIOSVersionID.Int64 == v.ID {
		_ = s.db.SetAppCurrentVersion(v.AppID, 0, "ios")
	}
	if v.Platform == "android" && app.CurrentAndroidVersionID.Valid && app.CurrentAndroidVersionID.Int64 == v.ID {
		_ = s.db.SetAppCurrentVersion(v.AppID, 0, "android")
	}
	_ = s.storage.DeleteVersionFiles(v)
	if err := s.db.DeleteVersion(v.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/apps/%d", v.AppID), http.StatusSeeOther)
}

// ---- UDIDs / Stats ----

func (s *Server) handleAdminAppUDIDs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	app, err := s.db.GetApp(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	list, _ := s.db.ListUDIDs(id)

	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="udids-%s.csv"`, app.ShortID))
		cw := csv.NewWriter(w)
		cw.Write([]string{"udid", "product", "ios_version", "serial", "imei", "collected_at"})
		for _, u := range list {
			cw.Write([]string{
				u.UDID, u.Product, u.Version, u.Serial, u.IMEI,
				strconv.FormatInt(u.CollectedAt, 10),
			})
		}
		cw.Flush()
		return
	}

	u, _ := userFromContext(r.Context())
	s.renderHTML(w, http.StatusOK, "admin_udids.html", map[string]any{
		"User":  u,
		"Site":  s.cfg.Site,
		"App":   app,
		"UDIDs": list,
	})
}

// ---- Users (admin-only) ----

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{2,32}$`)

type userRow struct {
	User       *db.User
	TokenCount int
	IsSelf     bool
}

func (s *Server) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	me, _ := userFromContext(r.Context())
	users, err := s.db.ListUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]userRow, 0, len(users))
	for _, u := range users {
		cnt, _ := s.db.CountTokensByUser(u.ID)
		rows = append(rows, userRow{User: u, TokenCount: cnt, IsSelf: u.ID == me.ID})
	}
	// Notice flash via cookie (one-shot)
	var notice, generatedPassword string
	if c, err := r.Cookie("distsrv_user_notice"); err == nil && c.Value != "" {
		if dec, err := url.QueryUnescape(c.Value); err == nil {
			notice = dec
		} else {
			notice = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: "distsrv_user_notice", Value: "", Path: "/admin/users", MaxAge: -1})
	}
	if c, err := r.Cookie("distsrv_new_user_pw"); err == nil && c.Value != "" {
		generatedPassword = c.Value
		http.SetCookie(w, &http.Cookie{Name: "distsrv_new_user_pw", Value: "", Path: "/admin/users", MaxAge: -1})
	}
	s.renderHTML(w, http.StatusOK, "admin_users.html", map[string]any{
		"User":              me,
		"Site":              s.cfg.Site,
		"Rows":              rows,
		"Notice":            notice,
		"GeneratedPassword": generatedPassword,
	})
}

func (s *Server) handleAdminUsersCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on" || r.FormValue("is_admin") == "1"
	if !usernameRe.MatchString(username) {
		http.Error(w, "用户名只能含字母数字 _ . -，长度 2-32", http.StatusBadRequest)
		return
	}
	if _, err := s.db.GetUserByUsername(username); err == nil {
		http.Error(w, "用户名已存在", http.StatusConflict)
		return
	}
	generated := ""
	if password == "" {
		token, err := auth.RandomToken(9)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		password = token[:16]
		generated = password
	} else if len(password) < 8 {
		http.Error(w, "密码至少 8 位", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(password, s.cfg.Security.BcryptCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.db.CreateUser(username, hash, isAdmin); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if generated != "" {
		http.SetCookie(w, &http.Cookie{
			Name: "distsrv_new_user_pw", Value: username + "|" + generated,
			Path: "/admin/users", MaxAge: 60, HttpOnly: true,
			Secure: !s.cfg.Server.DevMode, SameSite: http.SameSiteLaxMode,
		})
	} else {
		flashNotice(w, "已创建用户 "+username)
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleAdminUsersToggleAdmin(w http.ResponseWriter, r *http.Request) {
	me, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id == me.ID {
		http.Error(w, "不能修改自己的管理员状态", http.StatusBadRequest)
		return
	}
	target, err := s.db.GetUserByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	makeAdmin := !target.IsAdmin
	// If demoting an admin, make sure at least one active admin remains.
	if !makeAdmin {
		n, _ := s.db.CountActiveAdmins()
		if n <= 1 {
			http.Error(w, "至少保留一个有效管理员，无法撤销", http.StatusBadRequest)
			return
		}
	}
	if err := s.db.SetUserAdmin(id, makeAdmin); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flashNotice(w, fmt.Sprintf("已%s用户 %s 的管理员权限", boolWord(makeAdmin, "授予", "撤销"), target.Username))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleAdminUsersToggleDisabled(w http.ResponseWriter, r *http.Request) {
	me, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id == me.ID {
		http.Error(w, "不能禁用自己", http.StatusBadRequest)
		return
	}
	target, err := s.db.GetUserByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	disable := !target.Disabled()
	if disable && target.IsAdmin {
		n, _ := s.db.CountActiveAdmins()
		if n <= 1 {
			http.Error(w, "禁用此用户会导致没有可用管理员，先指派其他管理员", http.StatusBadRequest)
			return
		}
	}
	if err := s.db.SetUserDisabled(id, disable); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flashNotice(w, fmt.Sprintf("已%s用户 %s", boolWord(disable, "禁用", "启用"), target.Username))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleAdminUsersResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	target, err := s.db.GetUserByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	token, err := auth.RandomToken(9)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newPw := token[:16]
	hash, err := auth.HashPassword(newPw, s.cfg.Security.BcryptCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.UpdateUserPassword(id, hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "distsrv_new_user_pw", Value: target.Username + "|" + newPw,
		Path: "/admin/users", MaxAge: 60, HttpOnly: true,
		Secure: !s.cfg.Server.DevMode, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleAdminUsersDelete(w http.ResponseWriter, r *http.Request) {
	me, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id == me.ID {
		http.Error(w, "不能删除自己", http.StatusBadRequest)
		return
	}
	target, err := s.db.GetUserByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if target.IsAdmin && !target.Disabled() {
		n, _ := s.db.CountActiveAdmins()
		if n <= 1 {
			http.Error(w, "至少保留一个管理员，无法删除", http.StatusBadRequest)
			return
		}
	}
	if err := s.db.DeleteUser(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flashNotice(w, fmt.Sprintf("已删除用户 %s", target.Username))
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func flashNotice(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: "distsrv_user_notice", Value: url.QueryEscape(msg),
		Path: "/admin/users", MaxAge: 30, HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func boolWord(b bool, ifTrue, ifFalse string) string {
	if b {
		return ifTrue
	}
	return ifFalse
}

// ---- API Tokens ----

func (s *Server) handleAdminTokensList(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	tokens, _ := s.db.ListAPITokens(u.ID)
	// Optional: a freshly-created plaintext token is passed via cookie (one-time display)
	var newToken string
	if c, err := r.Cookie("distsrv_new_token"); err == nil && c.Value != "" {
		newToken = c.Value
		// clear it
		http.SetCookie(w, &http.Cookie{Name: "distsrv_new_token", Value: "", Path: "/admin/tokens", MaxAge: -1})
	}
	s.renderHTML(w, http.StatusOK, "admin_tokens.html", map[string]any{
		"User":     u,
		"Site":     s.cfg.Site,
		"Tokens":   tokens,
		"NewToken": newToken,
		"BaseURL":  s.baseURL(),
	})
}

func (s *Server) handleAdminTokensCreate(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	raw, err := auth.RandomToken(32) // 64 hex chars
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plaintext := "dst_" + raw
	sum := sha256.Sum256([]byte(plaintext))
	hash := hex.EncodeToString(sum[:])
	prefix := plaintext
	if len(prefix) > 10 {
		prefix = plaintext[:10]
	}
	id, err := s.db.CreateAPIToken(u.ID, name, hash, prefix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// CI/CLI callers: return JSON with plaintext token directly.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":    id,
			"name":  name,
			"token": plaintext,
		})
		return
	}

	// Browser flow: one-time display via short-lived cookie + redirect.
	http.SetCookie(w, &http.Cookie{
		Name:     "distsrv_new_token",
		Value:    plaintext,
		Path:     "/admin/tokens",
		MaxAge:   60,
		HttpOnly: true,
		Secure:   !s.cfg.Server.DevMode,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/tokens", http.StatusSeeOther)
}

func (s *Server) handleAdminTokensDelete(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.db.DeleteAPIToken(id, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/tokens", http.StatusSeeOther)
}

func (s *Server) handleAdminAppStats(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	app, err := s.db.GetApp(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}
	rows, _ := s.db.DownloadsByDay(id, days)
	u, _ := userFromContext(r.Context())
	s.renderHTML(w, http.StatusOK, "admin_stats.html", map[string]any{
		"User": u,
		"Site": s.cfg.Site,
		"App":  app,
		"Rows": rows,
		"Days": days,
	})
}

