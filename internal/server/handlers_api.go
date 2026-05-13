package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"distsrv/internal/db"
)

type apiCtxKey int

const apiUserKey apiCtxKey = iota

// requireAPIToken authenticates requests using Authorization: Bearer <token>.
func (s *Server) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			writeAPIError(w, http.StatusUnauthorized, "missing Bearer token in Authorization header")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
		if token == "" {
			writeAPIError(w, http.StatusUnauthorized, "empty token")
			return
		}
		sum := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(sum[:])
		t, err := s.db.GetAPITokenByHash(hash)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		u, err := s.db.GetUserByID(t.UserID)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "token user gone")
			return
		}
		if u.Disabled() {
			writeAPIError(w, http.StatusUnauthorized, "token user disabled")
			return
		}
		_ = s.db.TouchAPIToken(t.ID)
		ctx := context.WithValue(r.Context(), apiUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func apiUser(ctx context.Context) *db.User {
	if u, ok := ctx.Value(apiUserKey).(*db.User); ok {
		return u
	}
	return nil
}

// ---- response helpers ----

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- types ----

type apiAppView struct {
	ID            int64    `json:"id"`
	ShortID       string   `json:"short_id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	IOSBundleID   string   `json:"ios_bundle_id,omitempty"`
	AndroidPkg    string   `json:"android_package,omitempty"`
	IOSVersion    *apiVer  `json:"ios_current,omitempty"`
	AndroidVer    *apiVer  `json:"android_current,omitempty"`
	DownloadPage  string   `json:"download_page_url"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

type apiVer struct {
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

func (s *Server) buildAppView(a *db.App) apiAppView {
	v := apiAppView{
		ID:           a.ID,
		ShortID:      a.ShortID,
		Name:         a.Name,
		Description:  a.Description,
		IOSBundleID:  a.IOSBundleID,
		AndroidPkg:   a.AndroidPkg,
		DownloadPage: s.baseURL() + "/d/" + a.ShortID,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
	if a.CurrentIOSVersionID.Valid {
		if ver, err := s.db.GetVersion(a.CurrentIOSVersionID.Int64); err == nil {
			v.IOSVersion = s.buildVerView(ver)
		}
	}
	if a.CurrentAndroidVersionID.Valid {
		if ver, err := s.db.GetVersion(a.CurrentAndroidVersionID.Int64); err == nil {
			v.AndroidVer = s.buildVerView(ver)
		}
	}
	return v
}

func (s *Server) buildVerView(v *db.Version) *apiVer {
	return &apiVer{
		ID:          v.ID,
		Platform:    v.Platform,
		VersionName: v.VersionName,
		VersionCode: v.VersionCode,
		BundleID:    v.BundleID,
		FileSize:    v.FileSize,
		FileSHA256:  v.FileSHA256,
		UploadedAt:  v.UploadedAt,
		DownloadURL: fmt.Sprintf("%s/file/%d/%s", s.baseURL(), v.ID, baseFileName(v.FilePath)),
	}
}

// ---- handlers ----

func (s *Server) handleAPIWhoami(w http.ResponseWriter, r *http.Request) {
	u := apiUser(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       u.ID,
		"username": u.Username,
	})
}

func (s *Server) handleAPIServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":   s.baseURL(),
		"dev_mode":   s.cfg.Server.DevMode,
		"org_name":   s.cfg.Site.OrgName,
		"max_upload": s.cfg.Storage.MaxUploadMB,
	})
}

func (s *Server) handleAPIListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.db.ListApps()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]apiAppView, 0, len(apps))
	for _, a := range apps {
		out = append(out, s.buildAppView(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) handleAPIGetApp(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "shortID")
	a, err := s.db.GetAppByShortID(short)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return
	}
	writeJSON(w, http.StatusOK, s.buildAppView(a))
}

func (s *Server) handleAPICreateApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShortID     string `json:"short_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.ShortID = strings.TrimSpace(req.ShortID)
	req.Name = strings.TrimSpace(req.Name)
	if !shortIDRe.MatchString(req.ShortID) {
		writeAPIError(w, http.StatusBadRequest, "short_id must match [a-zA-Z0-9_-]{2,32}")
		return
	}
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "name required")
		return
	}
	if existing, err := s.db.GetAppByShortID(req.ShortID); err == nil && existing != nil {
		writeAPIError(w, http.StatusConflict, "short_id already exists")
		return
	}
	id, err := s.db.CreateApp(req.ShortID, req.Name, req.Description)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a, _ := s.db.GetApp(id)
	writeJSON(w, http.StatusCreated, s.buildAppView(a))
}

func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	short := chi.URLParam(r, "shortID")
	app, err := s.db.GetAppByShortID(short)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return
	}
	user := apiUser(r.Context())

	maxBytes := int64(s.cfg.Storage.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "expected multipart/form-data with field 'file'")
		return
	}

	var uploaded *apiVer
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "read upload: "+err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		res, err := s.storage.SaveUpload(part, app.ID, user.ID)
		_ = part.Close()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		uploaded = s.buildVerView(res.Version)
	}
	if uploaded == nil {
		writeAPIError(w, http.StatusBadRequest, "no 'file' part found in multipart body")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"version":      uploaded,
		"download_page": s.baseURL() + "/d/" + app.ShortID,
	})
}

func baseFileName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
