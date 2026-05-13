package db

const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    disabled_at   INTEGER,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

CREATE TABLE IF NOT EXISTS apps (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    short_id        TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    ios_bundle_id   TEXT NOT NULL DEFAULT '',
    android_pkg     TEXT NOT NULL DEFAULT '',
    current_ios_version_id     INTEGER,
    current_android_version_id INTEGER,
    password_hash   TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apps_short_id ON apps(short_id);

CREATE TABLE IF NOT EXISTS versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    platform        TEXT NOT NULL CHECK(platform IN ('ios','android')),
    version_name    TEXT NOT NULL,
    version_code    TEXT NOT NULL,
    bundle_id       TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    file_size       INTEGER NOT NULL,
    file_sha256     TEXT NOT NULL,
    icon_path       TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    uploaded_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    uploaded_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_versions_app_platform ON versions(app_id, platform, uploaded_at DESC);

CREATE TABLE IF NOT EXISTS downloads (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id   INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    platform     TEXT NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    udid         TEXT NOT NULL DEFAULT '',
    occurred_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_downloads_app_time ON downloads(app_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS udids (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    udid         TEXT NOT NULL,
    product      TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    serial       TEXT NOT NULL DEFAULT '',
    imei         TEXT NOT NULL DEFAULT '',
    collected_at INTEGER NOT NULL,
    UNIQUE(app_id, udid)
);

CREATE TABLE IF NOT EXISTS sessions (
    token       TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
`

func (d *DB) migrate() error {
	if _, err := d.Exec(schemaSQL); err != nil {
		return err
	}
	// Idempotent column adds for upgrades from versions before is_admin/disabled_at.
	if err := d.addColumnIfMissing("users", "is_admin", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.addColumnIfMissing("users", "disabled_at", "INTEGER"); err != nil {
		return err
	}
	// Backfill: if any user exists but none is admin, promote the oldest user
	// (this preserves the "single-admin" expectation for pre-multiuser installs).
	if err := d.promoteFirstUserIfNoAdmin(); err != nil {
		return err
	}
	return nil
}

func (d *DB) addColumnIfMissing(table, col, decl string) error {
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}
	_, err = d.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + decl)
	return err
}

func (d *DB) promoteFirstUserIfNoAdmin() error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	var admins int
	if err := d.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1").Scan(&admins); err != nil {
		return err
	}
	if admins > 0 {
		return nil
	}
	_, err := d.Exec("UPDATE users SET is_admin = 1 WHERE id = (SELECT MIN(id) FROM users)")
	return err
}
