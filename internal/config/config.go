// Package config loads, validates, and saves the TOML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

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
}

// Load decodes and validates a config file.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config as TOML.
func Save(path string, c *Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

// Validate checks required fields, name uniqueness, and identity existence.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for i, s := range c.Sync {
		if s.Name == "" {
			return fmt.Errorf("sync[%d]: name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("sync[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
		for field, val := range map[string]string{
			"host": s.Host, "user": s.User,
			"remote_path": s.RemotePath, "local_path": s.LocalPath,
		} {
			if val == "" {
				return fmt.Errorf("sync %q: %s is required", s.Name, field)
			}
		}
		if s.Identity != "" {
			if _, err := os.Stat(ExpandHome(s.Identity)); err != nil {
				return fmt.Errorf("sync %q: identity not accessible: %w", s.Name, err)
			}
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
