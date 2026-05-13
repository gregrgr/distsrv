package db

import (
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

// ---------- Users ----------

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	DisabledAt   sql.NullInt64
	CreatedAt    int64
	LastLoginAt  sql.NullInt64
}

func (u *User) Disabled() bool { return u.DisabledAt.Valid }

const userColumns = "id, username, password_hash, is_admin, disabled_at, created_at, last_login_at"

func scanUser(row interface {
	Scan(dest ...any) error
}) (*User, error) {
	var u User
	var isAdmin int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &u.DisabledAt, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = isAdmin != 0
	return &u, nil
}

func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (d *DB) CountActiveAdmins() (int, error) {
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1 AND disabled_at IS NULL").Scan(&n)
	return n, err
}

func (d *DB) CreateUser(username, passwordHash string, isAdmin bool) (int64, error) {
	admin := 0
	if isAdmin {
		admin = 1
	}
	res, err := d.Exec(
		"INSERT INTO users(username, password_hash, is_admin, created_at) VALUES(?,?,?,?)",
		username, passwordHash, admin, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetUserByUsername(username string) (*User, error) {
	return scanUser(d.QueryRow("SELECT "+userColumns+" FROM users WHERE username = ?", username))
}

func (d *DB) GetUserByID(id int64) (*User, error) {
	return scanUser(d.QueryRow("SELECT "+userColumns+" FROM users WHERE id = ?", id))
}

func (d *DB) ListUsers() ([]*User, error) {
	rows, err := d.Query("SELECT " + userColumns + " FROM users ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

func (d *DB) UpdateUserPassword(id int64, passwordHash string) error {
	_, err := d.Exec("UPDATE users SET password_hash = ? WHERE id = ?", passwordHash, id)
	return err
}

func (d *DB) SetUserAdmin(id int64, isAdmin bool) error {
	v := 0
	if isAdmin {
		v = 1
	}
	_, err := d.Exec("UPDATE users SET is_admin = ? WHERE id = ?", v, id)
	return err
}

func (d *DB) SetUserDisabled(id int64, disabled bool) error {
	if disabled {
		_, err := d.Exec("UPDATE users SET disabled_at = ? WHERE id = ?", time.Now().Unix(), id)
		// also kick all active sessions of this user
		if err == nil {
			_, err = d.Exec("DELETE FROM sessions WHERE user_id = ?", id)
		}
		return err
	}
	_, err := d.Exec("UPDATE users SET disabled_at = NULL WHERE id = ?", id)
	return err
}

func (d *DB) DeleteUser(id int64) error {
	_, err := d.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

func (d *DB) TouchUserLogin(id int64) error {
	_, err := d.Exec("UPDATE users SET last_login_at = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

func (d *DB) CountTokensByUser(userID int64) (int, error) {
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM api_tokens WHERE user_id = ?", userID).Scan(&n)
	return n, err
}

// ---------- Apps ----------

type App struct {
	ID                      int64
	ShortID                 string
	Name                    string
	Description             string
	IOSBundleID             string
	AndroidPkg              string
	CurrentIOSVersionID     sql.NullInt64
	CurrentAndroidVersionID sql.NullInt64
	PasswordHash            string
	CreatedAt               int64
	UpdatedAt               int64
}

func (d *DB) CreateApp(shortID, name, description string) (int64, error) {
	now := time.Now().Unix()
	res, err := d.Exec(
		`INSERT INTO apps(short_id, name, description, created_at, updated_at)
		 VALUES(?,?,?,?,?)`,
		shortID, name, description, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetApp(id int64) (*App, error) {
	return d.queryApp("SELECT id, short_id, name, description, ios_bundle_id, android_pkg, current_ios_version_id, current_android_version_id, password_hash, created_at, updated_at FROM apps WHERE id = ?", id)
}

func (d *DB) GetAppByShortID(shortID string) (*App, error) {
	return d.queryApp("SELECT id, short_id, name, description, ios_bundle_id, android_pkg, current_ios_version_id, current_android_version_id, password_hash, created_at, updated_at FROM apps WHERE short_id = ?", shortID)
}

func (d *DB) queryApp(query string, arg any) (*App, error) {
	var a App
	err := d.QueryRow(query, arg).Scan(
		&a.ID, &a.ShortID, &a.Name, &a.Description,
		&a.IOSBundleID, &a.AndroidPkg,
		&a.CurrentIOSVersionID, &a.CurrentAndroidVersionID,
		&a.PasswordHash, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (d *DB) ListApps() ([]*App, error) {
	rows, err := d.Query(`SELECT id, short_id, name, description, ios_bundle_id, android_pkg,
		current_ios_version_id, current_android_version_id, password_hash, created_at, updated_at
		FROM apps ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*App
	for rows.Next() {
		var a App
		if err := rows.Scan(
			&a.ID, &a.ShortID, &a.Name, &a.Description,
			&a.IOSBundleID, &a.AndroidPkg,
			&a.CurrentIOSVersionID, &a.CurrentAndroidVersionID,
			&a.PasswordHash, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, &a)
	}
	return list, rows.Err()
}

func (d *DB) UpdateApp(id int64, name, description, shortID string) error {
	_, err := d.Exec(
		"UPDATE apps SET name = ?, description = ?, short_id = ?, updated_at = ? WHERE id = ?",
		name, description, shortID, time.Now().Unix(), id,
	)
	return err
}

func (d *DB) SetAppPassword(id int64, passwordHash string) error {
	_, err := d.Exec("UPDATE apps SET password_hash = ?, updated_at = ? WHERE id = ?",
		passwordHash, time.Now().Unix(), id)
	return err
}

func (d *DB) SetAppCurrentVersion(appID, versionID int64, platform string) error {
	var col string
	switch platform {
	case "ios":
		col = "current_ios_version_id"
	case "android":
		col = "current_android_version_id"
	default:
		return errors.New("invalid platform")
	}
	var arg any = versionID
	if versionID == 0 {
		arg = nil
	}
	_, err := d.Exec("UPDATE apps SET "+col+" = ?, updated_at = ? WHERE id = ?",
		arg, time.Now().Unix(), appID)
	return err
}

func (d *DB) SetAppBundleID(appID int64, platform, bundleID string) error {
	var col string
	switch platform {
	case "ios":
		col = "ios_bundle_id"
	case "android":
		col = "android_pkg"
	default:
		return errors.New("invalid platform")
	}
	_, err := d.Exec("UPDATE apps SET "+col+" = ?, updated_at = ? WHERE id = ?",
		bundleID, time.Now().Unix(), appID)
	return err
}

func (d *DB) DeleteApp(id int64) error {
	_, err := d.Exec("DELETE FROM apps WHERE id = ?", id)
	return err
}

// ---------- Versions ----------

type Version struct {
	ID          int64
	AppID       int64
	Platform    string
	VersionName string
	VersionCode string
	BundleID    string
	FilePath    string
	FileSize    int64
	FileSHA256  string
	IconPath    string
	Changelog   string
	UploadedBy  sql.NullInt64
	UploadedAt  int64
}

func (d *DB) CreateVersion(v *Version) (int64, error) {
	v.UploadedAt = time.Now().Unix()
	res, err := d.Exec(
		`INSERT INTO versions(app_id, platform, version_name, version_code, bundle_id,
		 file_path, file_size, file_sha256, icon_path, changelog, uploaded_by, uploaded_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.AppID, v.Platform, v.VersionName, v.VersionCode, v.BundleID,
		v.FilePath, v.FileSize, v.FileSHA256, v.IconPath, v.Changelog, v.UploadedBy, v.UploadedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetVersion(id int64) (*Version, error) {
	var v Version
	err := d.QueryRow(
		`SELECT id, app_id, platform, version_name, version_code, bundle_id,
		 file_path, file_size, file_sha256, icon_path, changelog, uploaded_by, uploaded_at
		 FROM versions WHERE id = ?`, id,
	).Scan(&v.ID, &v.AppID, &v.Platform, &v.VersionName, &v.VersionCode, &v.BundleID,
		&v.FilePath, &v.FileSize, &v.FileSHA256, &v.IconPath, &v.Changelog, &v.UploadedBy, &v.UploadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) ListVersions(appID int64, platform string) ([]*Version, error) {
	query := `SELECT id, app_id, platform, version_name, version_code, bundle_id,
	          file_path, file_size, file_sha256, icon_path, changelog, uploaded_by, uploaded_at
	          FROM versions WHERE app_id = ?`
	args := []any{appID}
	if platform != "" {
		query += " AND platform = ?"
		args = append(args, platform)
	}
	query += " ORDER BY uploaded_at DESC"
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.AppID, &v.Platform, &v.VersionName, &v.VersionCode, &v.BundleID,
			&v.FilePath, &v.FileSize, &v.FileSHA256, &v.IconPath, &v.Changelog, &v.UploadedBy, &v.UploadedAt); err != nil {
			return nil, err
		}
		list = append(list, &v)
	}
	return list, rows.Err()
}

func (d *DB) DeleteVersion(id int64) error {
	_, err := d.Exec("DELETE FROM versions WHERE id = ?", id)
	return err
}

// ListVersionsToPrune returns versions of (appID, platform) that exceed keepN,
// excluding the current pinned version id (if any).
func (d *DB) ListVersionsToPrune(appID int64, platform string, keepN int, currentID int64) ([]*Version, error) {
	all, err := d.ListVersions(appID, platform)
	if err != nil {
		return nil, err
	}
	// Build a list excluding the current pinned id, then keep the most recent keepN.
	var filtered []*Version
	for _, v := range all {
		if v.ID == currentID {
			continue
		}
		filtered = append(filtered, v)
	}
	if len(filtered) <= keepN {
		return nil, nil
	}
	return filtered[keepN:], nil
}

// ---------- Downloads ----------

func (d *DB) RecordDownload(appID, versionID int64, platform, ip, ua, udid string) error {
	_, err := d.Exec(
		`INSERT INTO downloads(app_id, version_id, platform, ip, user_agent, udid, occurred_at)
		 VALUES(?,?,?,?,?,?,?)`,
		appID, versionID, platform, ip, ua, udid, time.Now().Unix(),
	)
	return err
}

func (d *DB) CountDownloadsByVersion(versionID int64) (int, error) {
	var n int
	err := d.QueryRow("SELECT COUNT(*) FROM downloads WHERE version_id = ?", versionID).Scan(&n)
	return n, err
}

type DailyDownload struct {
	Day      string
	Platform string
	Count    int
}

func (d *DB) DownloadsByDay(appID int64, sinceDays int) ([]DailyDownload, error) {
	since := time.Now().Add(-time.Duration(sinceDays) * 24 * time.Hour).Unix()
	rows, err := d.Query(`
		SELECT strftime('%Y-%m-%d', datetime(occurred_at, 'unixepoch')) AS day, platform, COUNT(*)
		FROM downloads
		WHERE app_id = ? AND occurred_at >= ?
		GROUP BY day, platform
		ORDER BY day ASC`, appID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []DailyDownload
	for rows.Next() {
		var r DailyDownload
		if err := rows.Scan(&r.Day, &r.Platform, &r.Count); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// ---------- UDIDs ----------

type UDID struct {
	ID          int64
	AppID       int64
	UDID        string
	Product     string
	Version     string
	Serial      string
	IMEI        string
	CollectedAt int64
}

func (d *DB) UpsertUDID(u *UDID) error {
	u.CollectedAt = time.Now().Unix()
	_, err := d.Exec(`
		INSERT INTO udids(app_id, udid, product, version, serial, imei, collected_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(app_id, udid) DO UPDATE SET
		   product=excluded.product, version=excluded.version,
		   serial=excluded.serial, imei=excluded.imei,
		   collected_at=excluded.collected_at`,
		u.AppID, u.UDID, u.Product, u.Version, u.Serial, u.IMEI, u.CollectedAt)
	return err
}

func (d *DB) ListUDIDs(appID int64) ([]*UDID, error) {
	rows, err := d.Query(`SELECT id, app_id, udid, product, version, serial, imei, collected_at
		FROM udids WHERE app_id = ? ORDER BY collected_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*UDID
	for rows.Next() {
		var u UDID
		if err := rows.Scan(&u.ID, &u.AppID, &u.UDID, &u.Product, &u.Version, &u.Serial, &u.IMEI, &u.CollectedAt); err != nil {
			return nil, err
		}
		list = append(list, &u)
	}
	return list, rows.Err()
}

// ---------- Sessions ----------

type Session struct {
	Token     string
	UserID    int64
	ExpiresAt int64
	CreatedAt int64
}

func (d *DB) CreateSession(token string, userID int64, ttl time.Duration) error {
	now := time.Now()
	_, err := d.Exec(
		"INSERT INTO sessions(token, user_id, expires_at, created_at) VALUES(?,?,?,?)",
		token, userID, now.Add(ttl).Unix(), now.Unix(),
	)
	return err
}

func (d *DB) GetSession(token string) (*Session, error) {
	var s Session
	err := d.QueryRow(
		"SELECT token, user_id, expires_at, created_at FROM sessions WHERE token = ?",
		token,
	).Scan(&s.Token, &s.UserID, &s.ExpiresAt, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() > s.ExpiresAt {
		_, _ = d.Exec("DELETE FROM sessions WHERE token = ?", token)
		return nil, ErrNotFound
	}
	return &s, nil
}

func (d *DB) DeleteSession(token string) error {
	_, err := d.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

func (d *DB) PurgeExpiredSessions() error {
	_, err := d.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now().Unix())
	return err
}

// ---------- API Tokens ----------

type APIToken struct {
	ID         int64
	UserID     int64
	Name       string
	TokenHash  string
	Prefix     string
	CreatedAt  int64
	LastUsedAt sql.NullInt64
}

func (d *DB) CreateAPIToken(userID int64, name, tokenHash, prefix string) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO api_tokens(user_id, name, token_hash, prefix, created_at)
		 VALUES(?,?,?,?,?)`,
		userID, name, tokenHash, prefix, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetAPITokenByHash(hash string) (*APIToken, error) {
	var t APIToken
	err := d.QueryRow(
		`SELECT id, user_id, name, token_hash, prefix, created_at, last_used_at
		 FROM api_tokens WHERE token_hash = ?`, hash,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) ListAPITokens(userID int64) ([]*APIToken, error) {
	rows, err := d.Query(
		`SELECT id, user_id, name, token_hash, prefix, created_at, last_used_at
		 FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		list = append(list, &t)
	}
	return list, rows.Err()
}

func (d *DB) DeleteAPIToken(id, userID int64) error {
	_, err := d.Exec("DELETE FROM api_tokens WHERE id = ? AND user_id = ?", id, userID)
	return err
}

func (d *DB) TouchAPIToken(id int64) error {
	_, err := d.Exec("UPDATE api_tokens SET last_used_at = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}
