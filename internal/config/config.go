package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Admin    AdminConfig    `toml:"admin"`
	Storage  StorageConfig  `toml:"storage"`
	DB       DBConfig       `toml:"db"`
	Site     SiteConfig     `toml:"site"`
	Security SecurityConfig `toml:"security"`
	Log      LogConfig      `toml:"log"`
}

type ServerConfig struct {
	Domain             string `toml:"domain"`
	HTTPAddr           string `toml:"http_addr"`
	HTTPSAddr          string `toml:"https_addr"`
	ACMEEmail          string `toml:"acme_email"`
	ACMECacheDir       string `toml:"acme_cache_dir"`
	ReadTimeoutSeconds int    `toml:"read_timeout_seconds"`
	DevMode            bool   `toml:"dev_mode"`
	DevAddr            string `toml:"dev_addr"`
}

type AdminConfig struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type StorageConfig struct {
	DataDir                 string `toml:"data_dir"`
	UploadsSubdir           string `toml:"uploads_subdir"`
	MaxUploadMB             int    `toml:"max_upload_mb"`
	KeepVersionsPerPlatform int    `toml:"keep_versions_per_platform"`
	LowDiskThresholdMB      int    `toml:"low_disk_threshold_mb"`
}

type DBConfig struct {
	Path           string `toml:"path"`
	BusyTimeoutMS  int    `toml:"busy_timeout_ms"`
}

type SiteConfig struct {
	OrgName        string `toml:"org_name"`
	OrgSlug        string `toml:"org_slug"`
	SupportContact string `toml:"support_contact"`
}

type SecurityConfig struct {
	SessionTTLHours int `toml:"session_ttl_hours"`
	BcryptCost      int `toml:"bcrypt_cost"`
}

type LogConfig struct {
	Level string `toml:"level"`
}

func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":80"
	}
	if c.Server.HTTPSAddr == "" {
		c.Server.HTTPSAddr = ":443"
	}
	if c.Server.ReadTimeoutSeconds == 0 {
		c.Server.ReadTimeoutSeconds = 300
	}
	if c.Storage.DataDir == "" {
		c.Storage.DataDir = "/var/lib/distsrv"
	}
	if c.Storage.UploadsSubdir == "" {
		c.Storage.UploadsSubdir = "uploads"
	}
	if c.Storage.MaxUploadMB == 0 {
		c.Storage.MaxUploadMB = 300
	}
	if c.Storage.KeepVersionsPerPlatform == 0 {
		c.Storage.KeepVersionsPerPlatform = 3
	}
	if c.Storage.LowDiskThresholdMB == 0 {
		c.Storage.LowDiskThresholdMB = 500
	}
	if c.DB.Path == "" {
		c.DB.Path = filepath.Join(c.Storage.DataDir, "distsrv.db")
	}
	if c.DB.BusyTimeoutMS == 0 {
		c.DB.BusyTimeoutMS = 5000
	}
	if c.Server.ACMECacheDir == "" {
		c.Server.ACMECacheDir = filepath.Join(c.Storage.DataDir, "certs")
	}
	if c.Security.SessionTTLHours == 0 {
		c.Security.SessionTTLHours = 168
	}
	if c.Security.BcryptCost == 0 {
		c.Security.BcryptCost = 10
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Server.DevAddr == "" {
		c.Server.DevAddr = ":8080"
	}
	if c.Site.OrgName == "" {
		c.Site.OrgName = "Internal"
	}
	if c.Site.OrgSlug == "" {
		c.Site.OrgSlug = "internal"
	}
}

func (c *Config) validate() error {
	if !c.Server.DevMode {
		if c.Server.Domain == "" {
			return errors.New("server.domain is required (or set server.dev_mode=true for HTTP)")
		}
		if c.Server.ACMEEmail == "" {
			return errors.New("server.acme_email is required when dev_mode is false")
		}
	}
	if c.Admin.Username == "" {
		return errors.New("admin.username is required for initial bootstrap")
	}
	if c.Admin.Password == "" {
		return errors.New("admin.password is required for initial bootstrap")
	}
	return nil
}

func (c *Config) UploadsDir() string {
	return filepath.Join(c.Storage.DataDir, c.Storage.UploadsSubdir)
}

func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.Storage.DataDir, c.UploadsDir(), c.Server.ACMECacheDir, filepath.Dir(c.DB.Path)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}
