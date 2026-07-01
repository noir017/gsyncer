package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeKey(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestLoadValidateAndEffective(t *testing.T) {
	key := writeKey(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	content := `
[defaults]
ssh_port = 2222
[defaults.retention]
recent = 7
monthly = 6
semiannual = 4
yearly = 3

[[sync]]
name = "web"
host = "1.2.3.4"
user = "deploy"
identity = "` + key + `"
remote_path = "/var/www/"
local_path = "/data/web"
ignore = ["*.log"]
[sync.retention]
recent = 14
`
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := c.Sync[0]
	if got := s.EffectivePort(c.Defaults); got != 2222 {
		t.Fatalf("port = %d, want 2222", got)
	}
	r := s.EffectiveRetention(c.Defaults)
	if r.Recent != 14 || r.Monthly != 6 || r.Semiannual != 4 || r.Yearly != 3 {
		t.Fatalf("retention = %+v", r)
	}
}

func TestValidateRejectsDuplicateName(t *testing.T) {
	key := writeKey(t)
	c := &Config{Sync: []Sync{
		{Name: "a", Host: "h", User: "u", Identity: key, RemotePath: "/r", LocalPath: "/l"},
		{Name: "a", Host: "h", User: "u", Identity: key, RemotePath: "/r", LocalPath: "/l2"},
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestValidateRejectsMissingField(t *testing.T) {
	c := &Config{Sync: []Sync{{Name: "a"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected missing-field error")
	}
}

func TestValidateRejectsNegativeDefaultRetention(t *testing.T) {
	c := &Config{
		Defaults: Defaults{Retention: Retention{Recent: -1}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for negative defaults retention")
	}
}

func TestValidateRejectsNegativeEntryRetention(t *testing.T) {
	neg := -1
	c := &Config{
		Sync: []Sync{{
			Name: "x", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/l",
			Retention: &RetentionOverride{Monthly: &neg},
		}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for negative per-entry retention override")
	}
}

func TestValidateRejectsRelativeLocalPath(t *testing.T) {
	c := &Config{Sync: []Sync{
		{Name: "a", Host: "h", User: "u", RemotePath: "/r", LocalPath: "data/web"},
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for relative local_path")
	}
}

func TestValidateRejectsRootLocalPath(t *testing.T) {
	c := &Config{Sync: []Sync{
		{Name: "a", Host: "h", User: "u", RemotePath: "/r", LocalPath: "/"},
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for filesystem-root local_path")
	}
}

func TestLoadExpandsTildeLocalPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	key := writeKey(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	content := `
[[sync]]
name = "web"
host = "h"
user = "u"
identity = "` + key + `"
remote_path = "/r"
local_path = "~/backups/web"
`
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join(home, "backups", "web")
	if c.Sync[0].LocalPath != want {
		t.Fatalf("local_path = %q, want %q", c.Sync[0].LocalPath, want)
	}
}

func TestValidateRejectsControlCharsInPaths(t *testing.T) {
	// LocalPath stays absolute so the abs-path check never masks the control-char
	// check we are exercising.
	cases := []struct {
		name          string
		remote, local string
	}{
		{"newline in remote_path", "/r\n/etc", "/l"},
		{"NUL in remote_path", "/r\x00", "/l"},
		{"control char in local_path", "/r", "/l\x1b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Sync: []Sync{
				{Name: "a", Host: "h", User: "u", RemotePath: tc.remote, LocalPath: tc.local},
			}}
			if err := c.Validate(); err == nil {
				t.Fatalf("expected control-char rejection for %q/%q", tc.remote, tc.local)
			}
		})
	}
}

func TestValidateWarnsOnOverPermissiveIdentity(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if err := os.WriteFile(key, []byte("x"), 0o644); err != nil { // group/other readable
		t.Fatal(err)
	}
	c := &Config{Sync: []Sync{
		{Name: "a", Host: "h", User: "u", Identity: key, RemotePath: "/r", LocalPath: "/l"},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("over-permissive key must warn, not error: %v", err)
	}
	if len(c.Warnings) == 0 {
		t.Fatal("expected a warning for 0644 identity key")
	}
}

func TestValidateNoWarningForPrivateIdentity(t *testing.T) {
	key := writeKey(t) // written 0600
	c := &Config{Sync: []Sync{
		{Name: "a", Host: "h", User: "u", Identity: key, RemotePath: "/r", LocalPath: "/l"},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("unexpected warnings for 0600 key: %v", c.Warnings)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	key := writeKey(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "out.toml")
	c := &Config{
		Defaults: Defaults{SSHPort: 22, Retention: Retention{Recent: 5}},
		Sync: []Sync{{Name: "a", Host: "h", User: "u", Identity: key,
			RemotePath: "/r", LocalPath: "/l"}},
	}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if c2.Sync[0].Name != "a" || c2.Defaults.Retention.Recent != 5 {
		t.Fatalf("roundtrip mismatch: %+v", c2)
	}
}
