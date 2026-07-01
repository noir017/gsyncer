// Package config loads, validates, and saves the TOML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// StarterTemplate is a commented example config written by `gsync init`. It
// decodes to a valid, empty-of-entries config (so `gsync list` works right
// after init); users uncomment the [[sync]] block to add their first entry.
const StarterTemplate = `# gsync 配置文件
# 每个 [[sync]] 块描述一个「远程目录 -> 本地目录」的备份任务。
# 去掉下面示例块的注释并按需修改即可。

[defaults]
  ssh_port = 22                    # 未在条目中指定时使用的默认 SSH 端口

  [defaults.retention]             # 默认 GFS 保留策略（条目可覆盖）
    recent     = 7                 # 保留最近的 7 份快照
    monthly    = 6                 # 含快照的最近 6 个月各留最新一份
    semiannual = 2
    yearly     = 2

[log]
  keep_days  = 30                  # 运行日志保留天数（0 = 不按天清理）
  keep_count = 100                 # 运行日志保留份数（0 = 不按份数清理）

# --- 示例条目（去掉注释后启用）---
# [[sync]]
#   name        = "web"
#   host        = "1.2.3.4"
#   port        = 22
#   user        = "deploy"
#   identity    = "~/.ssh/id_ed25519"
#   remote_path = "/srv/www"
#   local_path  = "/data/backups/web"   # 必须是绝对路径
#   strict_host_key = false
#   ignore      = ["node_modules/", "__pycache__/", "*.log"]
#
#   [sync.retention]                    # 可选：覆盖默认保留策略，未填字段回退到 defaults
#     recent = 14
`

// Retention is the resolved keep-count for each layer.
type Retention struct {
	Recent     int `toml:"recent"`
	Monthly    int `toml:"monthly"`
	Semiannual int `toml:"semiannual"`
	Yearly     int `toml:"yearly"`
}

// RetentionOverride is a partial retention; nil fields fall back to defaults.
type RetentionOverride struct {
	Recent     *int `toml:"recent"`
	Monthly    *int `toml:"monthly"`
	Semiannual *int `toml:"semiannual"`
	Yearly     *int `toml:"yearly"`
}

// LogConfig controls old-log cleanup.
type LogConfig struct {
	KeepDays  int `toml:"keep_days"`
	KeepCount int `toml:"keep_count"`
}

// Defaults holds project-wide defaults.
type Defaults struct {
	SSHPort   int       `toml:"ssh_port"`
	Retention Retention `toml:"retention"`
}

// Sync is one remote-folder sync entry.
type Sync struct {
	Name          string             `toml:"name"`
	Host          string             `toml:"host"`
	Port          int                `toml:"port"`
	User          string             `toml:"user"`
	Identity      string             `toml:"identity"`
	RemotePath    string             `toml:"remote_path"`
	LocalPath     string             `toml:"local_path"`
	Ignore        []string           `toml:"ignore"`
	StrictHostKey bool               `toml:"strict_host_key"`
	Retention     *RetentionOverride `toml:"retention"`
}

// Config is the whole file.
type Config struct {
	Defaults Defaults  `toml:"defaults"`
	Log      LogConfig `toml:"log"`
	Sync     []Sync    `toml:"sync"`
	// Warnings holds non-fatal issues surfaced at load time (e.g. an
	// over-permissive identity key). Not persisted: toml:"-" keeps Save from
	// writing it back into the config file.
	Warnings []string `toml:"-"`
}

// Load decodes and validates a config file.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config file not found at %s; run 'gsync init' to create one, or pass -config <path>", path)
		}
		return nil, err
	}
	// Expand a leading ~ in local_path so a value like "~/backups" resolves to a
	// real absolute directory instead of silently creating a literal "./~" tree
	// that rsync --delete then mirrors into.
	for i := range c.Sync {
		c.Sync[i].LocalPath = ExpandHome(c.Sync[i].LocalPath)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config as TOML atomically: it encodes to a temp file in the
// same directory and renames it into place, so a crash or disk-full mid-write
// can never truncate or corrupt the live config that cron runs depend on.
func Save(path string, c *Config) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// hasCtrlOrNUL reports whether s contains a NUL or ASCII control character.
// Such bytes in a path are almost always an injection attempt or corruption:
// remote_path is handed to a remote shell and local_path drives rsync --delete
// and prune, so a stray newline or NUL could truncate/redirect either.
func hasCtrlOrNUL(s string) bool {
	for _, r := range s {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// checkRetention returns an error if any retention field is negative.
func checkRetention(ctx string, r Retention) error {
	fields := []struct {
		name string
		val  int
	}{
		{"recent", r.Recent},
		{"monthly", r.Monthly},
		{"semiannual", r.Semiannual},
		{"yearly", r.Yearly},
	}
	for _, f := range fields {
		if f.val < 0 {
			return fmt.Errorf("%s: retention %s must be >= 0", ctx, f.name)
		}
	}
	return nil
}

// IdentityIssue returns a non-fatal description of a problem with the entry's
// identity file, or "" if the identity is empty or fine. An inaccessible key is
// an environment concern (e.g. editing a deploy config on another machine), not
// a structural config error, so callers treat this as a warning — see Validate.
func (s Sync) IdentityIssue() string {
	if s.Identity == "" {
		return ""
	}
	path := ExpandHome(s.Identity)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("identity %s not accessible: %v", path, err)
	}
	// A private key readable by group/other is a security hole; ssh may also
	// refuse to use it. Surface it rather than let a cron run fail opaquely.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("identity %s has group/other permissions (%#o); tighten to 0600", path, perm)
	}
	return ""
}

// Validate checks required fields, name uniqueness, and path sanity. Non-fatal
// issues (an inaccessible or over-permissive identity key) are collected into
// c.Warnings rather than returned as errors, so one entry with a missing key on
// the current machine never blocks loading the whole config.
func (c *Config) Validate() error {
	c.Warnings = nil
	seen := map[string]bool{}
	for i, s := range c.Sync {
		if s.Name == "" {
			return fmt.Errorf("sync[%d]: name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("sync[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
		required := []struct{ name, val string }{
			{"host", s.Host},
			{"user", s.User},
			{"remote_path", s.RemotePath},
			{"local_path", s.LocalPath},
		}
		for _, f := range required {
			if f.val == "" {
				return fmt.Errorf("sync %q: %s is required", s.Name, f.name)
			}
		}
		if hasCtrlOrNUL(s.RemotePath) {
			return fmt.Errorf("sync %q: remote_path contains a NUL or control character", s.Name)
		}
		if hasCtrlOrNUL(s.LocalPath) {
			return fmt.Errorf("sync %q: local_path contains a NUL or control character", s.Name)
		}
		// local_path is the snapshot root that rsync --delete and prune operate
		// on; a relative or root path here is a foot-gun (deletes into the cwd or
		// the whole filesystem), so require a real absolute path.
		if !filepath.IsAbs(s.LocalPath) {
			return fmt.Errorf("sync %q: local_path must be an absolute path, got %q", s.Name, s.LocalPath)
		}
		if filepath.Clean(s.LocalPath) == "/" {
			return fmt.Errorf("sync %q: local_path must not be the filesystem root", s.Name)
		}
		if msg := s.IdentityIssue(); msg != "" {
			c.Warnings = append(c.Warnings, fmt.Sprintf("sync %q: %s", s.Name, msg))
		}
	}
	if err := checkRetention("defaults", c.Defaults.Retention); err != nil {
		return err
	}
	for _, s := range c.Sync {
		if err := checkRetention(fmt.Sprintf("sync %q", s.Name), s.EffectiveRetention(c.Defaults)); err != nil {
			return err
		}
	}
	return nil
}

// EffectivePort resolves the port: entry > defaults > 22.
func (s Sync) EffectivePort(d Defaults) int {
	if s.Port != 0 {
		return s.Port
	}
	if d.SSHPort != 0 {
		return d.SSHPort
	}
	return 22
}

// EffectiveRetention merges the entry override over defaults.
func (s Sync) EffectiveRetention(d Defaults) Retention {
	r := d.Retention
	if s.Retention != nil {
		if s.Retention.Recent != nil {
			r.Recent = *s.Retention.Recent
		}
		if s.Retention.Monthly != nil {
			r.Monthly = *s.Retention.Monthly
		}
		if s.Retention.Semiannual != nil {
			r.Semiannual = *s.Retention.Semiannual
		}
		if s.Retention.Yearly != nil {
			r.Yearly = *s.Retention.Yearly
		}
	}
	return r
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
