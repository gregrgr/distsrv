package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"distsrv/internal/config"
	"distsrv/internal/db"
	"distsrv/internal/parser"
)

type Manager struct {
	cfg *config.Config
	db  *db.DB
}

func New(cfg *config.Config, database *db.DB) *Manager {
	return &Manager{cfg: cfg, db: database}
}

// UploadResult is the metadata returned after a successful upload.
type UploadResult struct {
	Version *db.Version
}

var (
	ErrUnsupported = errors.New("unsupported file type (must be .ipa or .apk)")
	ErrLowDisk     = errors.New("server is low on disk, upload blocked")
)

// SaveUpload streams a multipart part to disk, parses it, and inserts a version row.
// It deletes the temp file on any failure path.
func (m *Manager) SaveUpload(part *multipart.Part, appID int64, uploadedBy int64) (*UploadResult, error) {
	if err := m.CheckDiskSpace(); err != nil {
		return nil, err
	}

	filename := part.FileName()
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".ipa" && ext != ".apk" {
		return nil, ErrUnsupported
	}

	app, err := m.db.GetApp(appID)
	if err != nil {
		return nil, err
	}

	// Stream to a temp file in the uploads dir (so the final move is intra-fs, atomic).
	tmpDir := filepath.Join(m.cfg.UploadsDir(), ".tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir tmp: %w", err)
	}
	tmp, err := os.CreateTemp(tmpDir, "upload-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleanup if we don't end up moving the file.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	tee := io.TeeReader(part, h)
	buf := make([]byte, 32*1024)
	size, err := io.CopyBuffer(tmp, tee, buf)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("write upload: %w", err)
	}
	sha := hex.EncodeToString(h.Sum(nil))

	// Parse package metadata.
	var (
		platform    string
		bundleID    string
		versionName string
		versionCode string
		title       string
		iconBytes   []byte
	)
	switch ext {
	case ".ipa":
		info, perr := parser.ParseIPA(tmpPath)
		if perr != nil {
			return nil, fmt.Errorf("parse ipa: %w", perr)
		}
		platform = "ios"
		bundleID = info.BundleID
		versionName = info.ShortVersion
		versionCode = info.BundleVersion
		title = info.Title
		iconBytes = info.IconBytes
	case ".apk":
		info, perr := parser.ParseAPK(tmpPath)
		if perr != nil {
			return nil, fmt.Errorf("parse apk: %w", perr)
		}
		platform = "android"
		bundleID = info.Package
		versionName = info.VersionName
		versionCode = info.VersionCode
		title = info.Label
		iconBytes = info.IconBytes
	}
	_ = title

	if bundleID == "" {
		return nil, fmt.Errorf("could not extract bundle/package id from file")
	}

	// Move to final location: uploads/<app_id>/<platform>/<verName>-<ts>.<ext>
	ts := time.Now().Unix()
	finalDir := filepath.Join(m.cfg.UploadsDir(), fmt.Sprintf("%d", appID), platform)
	if err := os.MkdirAll(finalDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir final: %w", err)
	}
	safeName := sanitizeVersionForPath(versionName)
	if safeName == "" {
		safeName = "v"
	}
	finalName := fmt.Sprintf("%s-%d%s", safeName, ts, ext)
	finalPath := filepath.Join(finalDir, finalName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, fmt.Errorf("move file: %w", err)
	}
	committed = true

	// Save icon if available.
	var iconRel string
	if len(iconBytes) > 0 {
		iconRel = fmt.Sprintf("%d/%s/%s-%d.icon.png", appID, platform, safeName, ts)
		iconAbs := filepath.Join(m.cfg.UploadsDir(), iconRel)
		_ = os.WriteFile(iconAbs, iconBytes, 0o640)
	}

	rel, _ := filepath.Rel(m.cfg.UploadsDir(), finalPath)
	rel = filepath.ToSlash(rel)

	v := &db.Version{
		AppID:       appID,
		Platform:    platform,
		VersionName: versionName,
		VersionCode: versionCode,
		BundleID:    bundleID,
		FilePath:    rel,
		FileSize:    size,
		FileSHA256:  sha,
		IconPath:    iconRel,
	}
	if uploadedBy > 0 {
		v.UploadedBy = sql.NullInt64{Int64: uploadedBy, Valid: true}
	}
	id, err := m.db.CreateVersion(v)
	if err != nil {
		// Best-effort cleanup of files we just moved.
		_ = os.Remove(finalPath)
		if iconRel != "" {
			_ = os.Remove(filepath.Join(m.cfg.UploadsDir(), iconRel))
		}
		return nil, fmt.Errorf("insert version: %w", err)
	}
	v.ID = id

	// Always promote the freshly-uploaded version to "current" for this
	// platform. Treating every upload as a publish matches the expected UX
	// ("uploading a new build = users get the new build"); admins who need
	// to keep an older version pinned can still flip it back manually.
	_ = m.db.SetAppCurrentVersion(appID, id, platform)
	switch platform {
	case "ios":
		if app.IOSBundleID == "" {
			_ = m.db.SetAppBundleID(appID, "ios", bundleID)
		}
	case "android":
		if app.AndroidPkg == "" {
			_ = m.db.SetAppBundleID(appID, "android", bundleID)
		}
	}

	// Prune old versions for this (app, platform), keeping the configured count
	// plus the currently-pinned version (never pruned).
	if err := m.pruneOldVersions(appID, platform); err != nil {
		// non-fatal
		fmt.Fprintf(os.Stderr, "warning: prune old versions: %v\n", err)
	}

	return &UploadResult{Version: v}, nil
}

func (m *Manager) pruneOldVersions(appID int64, platform string) error {
	app, err := m.db.GetApp(appID)
	if err != nil {
		return err
	}
	var currentID int64
	switch platform {
	case "ios":
		if app.CurrentIOSVersionID.Valid {
			currentID = app.CurrentIOSVersionID.Int64
		}
	case "android":
		if app.CurrentAndroidVersionID.Valid {
			currentID = app.CurrentAndroidVersionID.Int64
		}
	}
	toDelete, err := m.db.ListVersionsToPrune(appID, platform, m.cfg.Storage.KeepVersionsPerPlatform, currentID)
	if err != nil {
		return err
	}
	for _, v := range toDelete {
		_ = m.DeleteVersionFiles(v)
		_ = m.db.DeleteVersion(v.ID)
	}
	return nil
}

func (m *Manager) DeleteVersionFiles(v *db.Version) error {
	root := m.cfg.UploadsDir()
	if v.FilePath != "" {
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(v.FilePath)))
	}
	if v.IconPath != "" {
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(v.IconPath)))
	}
	return nil
}

func (m *Manager) AbsPath(rel string) string {
	return filepath.Join(m.cfg.UploadsDir(), filepath.FromSlash(rel))
}

// CheckDiskSpace returns ErrLowDisk if free space on the data dir is below threshold.
func (m *Manager) CheckDiskSpace() error {
	threshold := int64(m.cfg.Storage.LowDiskThresholdMB) * 1024 * 1024
	free, err := freeBytes(m.cfg.Storage.DataDir)
	if err != nil {
		return nil // best-effort; don't fail upload because we can't statfs
	}
	if free < threshold {
		return ErrLowDisk
	}
	return nil
}

// FreeBytes returns free bytes for monitoring purposes (0 on error).
func (m *Manager) FreeBytes() int64 {
	n, err := freeBytes(m.cfg.Storage.DataDir)
	if err != nil {
		return 0
	}
	return n
}

func sanitizeVersionForPath(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// OrphanScan walks uploads/<appid>/<platform> and deletes files not referenced in versions.
func (m *Manager) OrphanScan() error {
	root := m.cfg.UploadsDir()
	known := map[string]struct{}{}
	versions := []string{}
	// Collect all referenced files from versions table.
	rows, err := m.db.Query("SELECT file_path, icon_path FROM versions")
	if err != nil {
		return err
	}
	for rows.Next() {
		var fp, ip string
		if err := rows.Scan(&fp, &ip); err != nil {
			rows.Close()
			return err
		}
		if fp != "" {
			known[filepath.ToSlash(fp)] = struct{}{}
		}
		if ip != "" {
			known[filepath.ToSlash(ip)] = struct{}{}
		}
		versions = append(versions, fp)
	}
	rows.Close()
	_ = versions

	// Walk uploads dir.
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasSuffix(p, ".tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, ok := known[rel]; ok {
			return nil
		}
		// Don't kill anything in the top-level dir or hidden temp.
		if strings.HasPrefix(rel, ".tmp/") {
			return nil
		}
		_ = os.Remove(p)
		return nil
	})
}

// freeBytes returns free bytes on the filesystem containing dir.
// Implementation is OS-specific (see storage_unix.go / storage_windows.go).
func freeBytes(dir string) (int64, error) {
	return platformFreeBytes(dir)
}
