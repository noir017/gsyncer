# gsync 核心 CLI 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建 gsync 的核心 CLI——通过 ssh+rsync 把多个远程文件夹单向拉取到本地，并维护带 GFS 保留策略的快照（hardlink/btrfs 双后端）。

**Architecture:** 纯函数模块（retention、ignore）+ 可注入命令执行器（execx）+ 快照后端抽象（snapshot）+ 编排层（syncer）+ 薄 CLI（main）。所有外部命令（rsync/ssh/btrfs/cp）经 `execx.Runner` 接口调用，测试用 `FakeRunner` 替身，无需真实网络/btrfs。

**Tech Stack:** Go 1.22，标准库 + `github.com/BurntSushi/toml`。`CGO_ENABLED=0` 静态单文件。TUI 不在本计划内（见末尾后续计划）。

## Global Constraints

- 语言 Go 1.22；`CGO_ENABLED=0 go build` 必须产出零依赖静态单文件。
- 唯一第三方依赖：`github.com/BurntSushi/toml`。不得引入其他依赖。
- 所有对 rsync/ssh/btrfs/cp 的调用必须经 `execx.Runner`，禁止在 syncer/snapshot 里直接 `os/exec`（除 execx.Real 内部）。
- 快照目录名格式固定 `2006-01-02_150405`（常量 `snapshot.TSLayout`）。
- 配置默认路径 = 可执行文件同目录 `config.toml`；日志目录 = 可执行文件同目录 `logs/`。
- 同步方向仅远程→本地，rsync 固定带 `-a --delete --info=stats2`。
- 条目级隔离：任一条目失败不影响其余；任一失败则进程退出码非 0。
- 每个任务结束必须 `gofmt` 通过且 `go test ./...` 全绿后再 commit。

---

## File Structure

```
gsync/
  go.mod
  .gitignore
  main.go                       CLI 入口、子命令分发、路径解析、汇总
  internal/execx/execx.go       Runner 接口、Real、FakeRunner
  internal/retention/retention.go  Policy、Select、Partition（纯函数）
  internal/ignore/ignore.go     ToRsyncFilters（gitignore→rsync filter）
  internal/config/config.go     类型、Load/Save/Validate、Effective*、ExpandHome
  internal/logx/logx.go         RunLogger、AppendSummary、Cleanup
  internal/snapshot/snapshot.go Backend 接口、Detect、List、TSLayout、FSType
  internal/snapshot/hardlink.go Hardlink 后端
  internal/snapshot/btrfs.go    Btrfs 后端
  internal/syncer/syncer.go     SyncOne/SyncMany、helpers（stats/ssh/rsync 参数）
  （对应 *_test.go 与各源文件同目录）
```

---

### Task 1: 项目脚手架

**Files:**
- Create: `gsync/go.mod`
- Create: `gsync/.gitignore`
- Create: `gsync/main.go`

**Interfaces:**
- Consumes: 无
- Produces: 可构建的 `gsync` 二进制，`gsync version` 打印版本。

- [ ] **Step 1: 初始化模块**

Run:
```bash
cd /home/user/work/gsync && go mod init gsync && go mod edit -go=1.22
```
Expected: 生成 `go.mod`，内容含 `module gsync` 与 `go 1.22`。

- [ ] **Step 2: 写 .gitignore**

Create `gsync/.gitignore`:
```
/gsync
/logs/
/config.toml
*.test
```

- [ ] **Step 3: 写最小 main.go**

Create `gsync/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		fmt.Println("gsync", version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: gsync <version>  (more commands added later)")
	os.Exit(2)
}
```

- [ ] **Step 4: 构建并运行**

Run:
```bash
cd /home/user/work/gsync && CGO_ENABLED=0 go build -o gsync . && ./gsync version
```
Expected: 输出 `gsync 0.1.0`，退出码 0。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add go.mod .gitignore main.go && git commit -m "chore: 项目脚手架与 version 命令"
```

---

### Task 2: execx 命令执行器

**Files:**
- Create: `gsync/internal/execx/execx.go`
- Test: `gsync/internal/execx/execx_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type Result struct { Stdout, Stderr string; Code int }`
  - `type Runner interface { Run(ctx context.Context, name string, args ...string) (Result, error) }`
  - `type Real struct{}`（实现 Runner，内部用 os/exec）
  - `type Call struct { Name string; Args []string }`
  - `type FakeRunner struct { Calls []Call; Handler func(name string, args []string) (Result, error) }`（实现 Runner）

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/execx/execx_test.go`:
```go
package execx

import (
	"context"
	"testing"
)

func TestRealRunCapturesStdout(t *testing.T) {
	var r Real
	res, err := r.Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "hello\n")
	}
}

func TestRealRunNonZeroExit(t *testing.T) {
	var r Real
	res, err := r.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if res.Code != 1 {
		t.Fatalf("code = %d, want 1", res.Code)
	}
}

func TestFakeRunnerRecordsAndResponds(t *testing.T) {
	f := &FakeRunner{Handler: func(name string, args []string) (Result, error) {
		return Result{Stdout: "ok"}, nil
	}}
	res, _ := f.Run(context.Background(), "rsync", "--version")
	if res.Stdout != "ok" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "rsync" || f.Calls[0].Args[0] != "--version" {
		t.Fatalf("calls not recorded: %+v", f.Calls)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/execx/`
Expected: 编译失败（未定义 Real/Result/FakeRunner）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/execx/execx.go`:
```go
// Package execx provides an injectable command runner so that callers can be
// tested without spawning real processes.
package execx

import (
	"bytes"
	"context"
	"os/exec"
)

// Result holds the captured output of a command.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner executes an external command.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// Real runs commands via os/exec.
type Real struct{}

// Run implements Runner.
func (Real) Run(ctx context.Context, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Code = ee.ExitCode()
	}
	return res, err
}

// Call records one invocation made against a FakeRunner.
type Call struct {
	Name string
	Args []string
}

// FakeRunner is a test double that records calls and returns scripted results.
type FakeRunner struct {
	Calls   []Call
	Handler func(name string, args []string) (Result, error)
}

// Run implements Runner.
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) (Result, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})
	if f.Handler != nil {
		return f.Handler(name, args)
	}
	return Result{}, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /home/user/work/gsync && go test ./internal/execx/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/execx && git commit -m "feat: execx 命令执行器与测试替身"
```

---

### Task 3: retention 保留算法

**Files:**
- Create: `gsync/internal/retention/retention.go`
- Test: `gsync/internal/retention/retention_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type Policy struct { Recent, Monthly, Semiannual, Yearly int }`
  - `func Select(times []time.Time, p Policy) []time.Time`（返回保留集，按时间降序）
  - `func Partition(times []time.Time, p Policy) (keep, del []time.Time)`

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/retention/retention_test.go`:
```go
package retention

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02_150405", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecentOnly(t *testing.T) {
	in := []time.Time{
		d("2026-06-01_000000"), d("2026-06-02_000000"), d("2026-06-03_000000"),
	}
	keep := Select(in, Policy{Recent: 2})
	if len(keep) != 2 || !keep[0].Equal(d("2026-06-03_000000")) || !keep[1].Equal(d("2026-06-02_000000")) {
		t.Fatalf("keep = %v", keep)
	}
}

func TestMonthlyCollapsesSameMonth(t *testing.T) {
	in := []time.Time{
		d("2026-06-01_000000"), d("2026-06-15_000000"), d("2026-05-10_000000"),
	}
	// monthly=2: keep newest of June and newest of May.
	keep := Select(in, Policy{Monthly: 2})
	if len(keep) != 2 {
		t.Fatalf("want 2, got %d: %v", len(keep), keep)
	}
	if !keep[0].Equal(d("2026-06-15_000000")) || !keep[1].Equal(d("2026-05-10_000000")) {
		t.Fatalf("keep = %v", keep)
	}
}

func TestUnionAcrossLayers(t *testing.T) {
	in := []time.Time{
		d("2026-06-20_000000"), d("2026-06-19_000000"), // recent
		d("2026-01-05_000000"),                         // older, distinct month/half
		d("2024-03-03_000000"),                         // distinct year
	}
	keep := Select(in, Policy{Recent: 1, Monthly: 1, Yearly: 2})
	// recent -> 06-20; monthly newest month -> 06-20 (dup); yearly newest 2 years
	// -> 2026 newest (06-20) + 2024 newest (2024-03-03).
	got := map[string]bool{}
	for _, k := range keep {
		got[k.Format("2006-01-02")] = true
	}
	if !got["2026-06-20"] || !got["2024-03-03"] {
		t.Fatalf("missing expected keeps: %v", keep)
	}
	if got["2026-06-19"] || got["2026-01-05"] {
		t.Fatalf("kept something it should drop: %v", keep)
	}
}

func TestPartitionAndEmpty(t *testing.T) {
	if k := Select(nil, Policy{Recent: 5}); len(k) != 0 {
		t.Fatalf("empty input should keep nothing: %v", k)
	}
	in := []time.Time{d("2026-06-01_000000"), d("2026-06-02_000000")}
	keep, del := Partition(in, Policy{Recent: 1})
	if len(keep) != 1 || len(del) != 1 {
		t.Fatalf("keep=%v del=%v", keep, del)
	}
	if !del[0].Equal(d("2026-06-01_000000")) {
		t.Fatalf("del = %v", del)
	}
}

func TestZeroPolicyKeepsNothing(t *testing.T) {
	in := []time.Time{d("2026-06-01_000000")}
	if k := Select(in, Policy{}); len(k) != 0 {
		t.Fatalf("zero policy should keep nothing: %v", k)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/retention/`
Expected: 编译失败（未定义 Select/Partition/Policy）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/retention/retention.go`:
```go
// Package retention implements grandfather-father-son snapshot retention.
package retention

import (
	"sort"
	"time"
)

// Policy is the number of snapshots to keep in each layer.
type Policy struct {
	Recent     int
	Monthly    int
	Semiannual int
	Yearly     int
}

// Select returns the snapshots to KEEP, sorted newest-first. The kept set is
// the union of four layers: the most recent N, plus the newest snapshot in each
// of the most recent monthly / semiannual / yearly buckets.
func Select(times []time.Time, p Policy) []time.Time {
	sorted := append([]time.Time(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].After(sorted[j]) })

	keep := map[int64]bool{}
	add := func(t time.Time) { keep[t.UnixNano()] = true }

	for i := 0; i < p.Recent && i < len(sorted); i++ {
		add(sorted[i])
	}

	pickBuckets := func(keyOf func(time.Time) int, n int) {
		if n <= 0 {
			return
		}
		seen := map[int]bool{}
		var order []int
		newest := map[int]time.Time{}
		for _, t := range sorted { // desc order: first seen per bucket is newest
			k := keyOf(t)
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
				newest[k] = t
			}
		}
		for i := 0; i < n && i < len(order); i++ {
			add(newest[order[i]])
		}
	}

	pickBuckets(func(t time.Time) int { return t.Year()*12 + int(t.Month()) - 1 }, p.Monthly)
	pickBuckets(func(t time.Time) int {
		h := 0
		if t.Month() > 6 {
			h = 1
		}
		return t.Year()*2 + h
	}, p.Semiannual)
	pickBuckets(func(t time.Time) int { return t.Year() }, p.Yearly)

	var out []time.Time
	for _, t := range sorted {
		if keep[t.UnixNano()] {
			out = append(out, t)
		}
	}
	return out
}

// Partition splits times into keep and delete sets (both newest-first).
func Partition(times []time.Time, p Policy) (keep, del []time.Time) {
	keep = Select(times, p)
	keepSet := map[int64]bool{}
	for _, t := range keep {
		keepSet[t.UnixNano()] = true
	}
	sorted := append([]time.Time(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].After(sorted[j]) })
	for _, t := range sorted {
		if !keepSet[t.UnixNano()] {
			del = append(del, t)
		}
	}
	return keep, del
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /home/user/work/gsync && go test ./internal/retention/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/retention && git commit -m "feat: GFS 快照保留算法"
```

---

### Task 4: ignore 规则翻译

**Files:**
- Create: `gsync/internal/ignore/ignore.go`
- Test: `gsync/internal/ignore/ignore_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func ToRsyncFilters(patterns []string) []string`

说明：gitignore 是「后匹配优先」，rsync filter 是「先匹配优先」，二者相反。翻译时**反转顺序**并映射 `!p`→`+ p`、其余 `p`→`- p`。跳过空行与 `#` 注释。已知边界（父目录被排除则无法重纳子文件）在文档说明，不在本任务处理。

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/ignore/ignore_test.go`:
```go
package ignore

import (
	"reflect"
	"testing"
)

func TestToRsyncFilters(t *testing.T) {
	in := []string{"*.log", "build/", "!important.log"}
	got := ToRsyncFilters(in)
	want := []string{"+ important.log", "- build/", "- *.log"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSkipsBlankAndComments(t *testing.T) {
	in := []string{"", "  ", "# comment", "*.tmp"}
	got := ToRsyncFilters(in)
	want := []string{"- *.tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEmpty(t *testing.T) {
	if got := ToRsyncFilters(nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/ignore/`
Expected: 编译失败（未定义 ToRsyncFilters）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/ignore/ignore.go`:
```go
// Package ignore translates gitignore-style patterns into ordered rsync filter
// rules. gitignore is last-match-wins; rsync is first-match-wins, so the order
// is reversed during translation.
package ignore

import "strings"

// ToRsyncFilters converts patterns into the strings passed as repeated --filter
// arguments. "!p" becomes an include ("+ p"); any other pattern becomes an
// exclude ("- p"). Blank lines and lines starting with '#' are skipped.
func ToRsyncFilters(patterns []string) []string {
	var out []string
	for i := len(patterns) - 1; i >= 0; i-- {
		p := strings.TrimSpace(patterns[i])
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if strings.HasPrefix(p, "!") {
			out = append(out, "+ "+strings.TrimSpace(p[1:]))
		} else {
			out = append(out, "- "+p)
		}
	}
	return out
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /home/user/work/gsync && go test ./internal/ignore/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/ignore && git commit -m "feat: gitignore 风格规则翻译为 rsync filter"
```

---

### Task 5: config 配置

**Files:**
- Create: `gsync/internal/config/config.go`
- Test: `gsync/internal/config/config_test.go`
- Modify: `gsync/go.mod`（添加 toml 依赖）

**Interfaces:**
- Consumes: 无
- Produces:
  - `type Retention struct { Recent, Monthly, Semiannual, Yearly int }`（toml: 同名小写）
  - `type RetentionOverride struct { Recent, Monthly, Semiannual, Yearly *int }`
  - `type LogConfig struct { KeepDays, KeepCount int }`
  - `type Defaults struct { SSHPort int; Retention Retention }`
  - `type Sync struct { Name, Host string; Port int; User, Identity, RemotePath, LocalPath string; Ignore []string; StrictHostKey bool; Retention *RetentionOverride }`
  - `type Config struct { Defaults Defaults; Log LogConfig; Sync []Sync }`
  - `func Load(path string) (*Config, error)`
  - `func Save(path string, c *Config) error`
  - `func (c *Config) Validate() error`
  - `func (s Sync) EffectivePort(d Defaults) int`
  - `func (s Sync) EffectiveRetention(d Defaults) Retention`
  - `func ExpandHome(p string) string`

- [ ] **Step 1: 添加 toml 依赖**

Run:
```bash
cd /home/user/work/gsync && go get github.com/BurntSushi/toml@v1.3.2
```
Expected: `go.mod` 出现 `require github.com/BurntSushi/toml v1.3.2`。

- [ ] **Step 2: 写失败测试**

Create `gsync/internal/config/config_test.go`:
```go
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
	if r.Recent != 14 || r.Monthly != 6 || r.Yearly != 3 {
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
```

- [ ] **Step 3: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/config/`
Expected: 编译失败（未定义类型/函数）。

- [ ] **Step 4: 写实现**

Create `gsync/internal/config/config.go`:
```go
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
```

- [ ] **Step 5: 运行测试确认通过**

Run: `cd /home/user/work/gsync && go test ./internal/config/`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
cd /home/user/work/gsync && git add go.mod go.sum internal/config && git commit -m "feat: TOML 配置加载/校验/默认值合并"
```

---

### Task 6: logx 日志

**Files:**
- Create: `gsync/internal/logx/logx.go`
- Test: `gsync/internal/logx/logx_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type RunLogger struct { ... }` 实现 `Infof(format string, a ...any)` 与 `Errorf(format string, a ...any)`
  - `func NewRunLogger(dir string, ts time.Time) (*RunLogger, error)`（写 `dir/<ts>.log`）
  - `func (l *RunLogger) Close() error`
  - `func AppendSummary(dir, line string) error`（追加到 `dir/summary.log`）
  - `func Cleanup(dir string, keepDays, keepCount int, now time.Time) error`

注：`Infof/Errorf` 的行内时间戳用 `time.Now()`（Go 程序允许）；测试只断言内容子串，不断言精确时间。Cleanup 仅处理可解析为 `TSLayout.log` 的文件，绝不删除 `summary.log`。

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/logx/logx_test.go`:
```go
package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, _ := time.Parse("2006-01-02_150405", s)
	return t
}

func TestRunLoggerWritesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRunLogger(dir, ts("2026-06-24_030000"))
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("hello %s", "world")
	l.Errorf("boom %d", 7)
	l.Close()
	data, err := os.ReadFile(filepath.Join(dir, "2026-06-24_030000.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "hello world") || !strings.Contains(s, "boom 7") {
		t.Fatalf("log content = %q", s)
	}
	if !strings.Contains(s, "INFO") || !strings.Contains(s, "ERROR") {
		t.Fatalf("missing levels: %q", s)
	}
}

func TestAppendSummary(t *testing.T) {
	dir := t.TempDir()
	if err := AppendSummary(dir, "run A ok"); err != nil {
		t.Fatal(err)
	}
	if err := AppendSummary(dir, "run B ok"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "summary.log"))
	if c := strings.Count(string(data), "\n"); c != 2 {
		t.Fatalf("want 2 lines, content=%q", data)
	}
}

func TestCleanupByCountAndDays(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"2026-06-24_030000.log",
		"2026-06-23_030000.log",
		"2026-01-01_030000.log", // old
		"summary.log",           // must survive
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := ts("2026-06-24_040000")
	// keepDays=30 removes the Jan file; keepCount=10 keeps the rest.
	if err := Cleanup(dir, 30, 10, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-01-01_030000.log")); err == nil {
		t.Fatal("old log should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.log")); err != nil {
		t.Fatal("summary.log must survive")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-06-24_030000.log")); err != nil {
		t.Fatal("recent log must survive")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/logx/`
Expected: 编译失败。

- [ ] **Step 3: 写实现**

Create `gsync/internal/logx/logx.go`:
```go
// Package logx provides per-run logging, a summary log, and old-log cleanup.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const tsLayout = "2006-01-02_150405"

// RunLogger writes one log file for a single run.
type RunLogger struct {
	f *os.File
}

// NewRunLogger creates dir (if needed) and opens dir/<ts>.log.
func NewRunLogger(dir string, ts time.Time) (*RunLogger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, ts.Format(tsLayout)+".log"))
	if err != nil {
		return nil, err
	}
	return &RunLogger{f: f}, nil
}

func (l *RunLogger) write(level, format string, a ...any) {
	fmt.Fprintf(l.f, "%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, fmt.Sprintf(format, a...))
}

// Infof logs at INFO level.
func (l *RunLogger) Infof(format string, a ...any) { l.write("INFO", format, a...) }

// Errorf logs at ERROR level.
func (l *RunLogger) Errorf(format string, a ...any) { l.write("ERROR", format, a...) }

// Close closes the underlying file.
func (l *RunLogger) Close() error { return l.f.Close() }

// AppendSummary appends one line to dir/summary.log.
func AppendSummary(dir, line string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "summary.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// Cleanup deletes per-run logs beyond keepCount or older than keepDays.
// summary.log and unparseable files are never touched. A zero limit disables
// that rule.
func Cleanup(dir string, keepDays, keepCount int, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type item struct {
		path string
		t    time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "summary.log" || !strings.HasSuffix(name, ".log") {
			continue
		}
		t, err := time.Parse(tsLayout, strings.TrimSuffix(name, ".log"))
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, name), t})
	}
	// newest first
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].t.After(items[i].t) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	for i, it := range items {
		del := false
		if keepCount > 0 && i >= keepCount {
			del = true
		}
		if keepDays > 0 && now.Sub(it.t) > time.Duration(keepDays)*24*time.Hour {
			del = true
		}
		if del {
			_ = os.Remove(it.path)
		}
	}
	return nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /home/user/work/gsync && go test ./internal/logx/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/logx && git commit -m "feat: 每次运行日志、汇总与旧日志清理"
```

---

### Task 7: snapshot 接口、探测与共享列举

**Files:**
- Create: `gsync/internal/snapshot/snapshot.go`
- Test: `gsync/internal/snapshot/snapshot_test.go`

**Interfaces:**
- Consumes: `execx.Runner`（Task 2）
- Produces:
  - `const TSLayout = "2006-01-02_150405"`
  - `const BtrfsMagic = 0x9123683E`
  - `type FSTypeFunc func(path string) (int64, error)`
  - `func RealFSType(path string) (int64, error)`
  - `type Backend interface { Name() string; EnsureCurrent(ctx, root) (string,error); Create(ctx, root, ts) (string,error); Delete(ctx, snapPath) error; List(root) ([]time.Time,error) }`
  - `func List(root string) ([]time.Time, error)`（共享，解析 `root/snapshots/*` 目录名）
  - `func Detect(ctx context.Context, root string, r execx.Runner, fsType FSTypeFunc) Backend`（在 Task 8/9 引入 Hardlink/Btrfs 后才能返回它们；本任务先定义接口、List、Detect 框架，Detect 暂返回 nil 占位由 Task 9 完成接线——见下）

为避免跨任务循环，本任务的 `Detect` 直接引用将在 Task 8/9 定义的 `NewHardlink`/`NewBtrfs`。**因此 Task 7、8、9 共属一个编译单元（同一 package snapshot），三者全部完成后该 package 才编译通过。** 每个任务末尾的测试命令只跑本 package，故 Task 7 的测试在 Task 8/9 完成前会因 `Detect` 引用未定义符号而编译失败——**Task 7 提交时先不写 Detect 实体，仅写接口/List/常量/FSType，Detect 留到 Task 9**。

- [ ] **Step 1: 写失败测试（仅覆盖 List 与常量）**

Create `gsync/internal/snapshot/snapshot_test.go`:
```go
package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListParsesSnapshotDirs(t *testing.T) {
	root := t.TempDir()
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(filepath.Join(snaps, "2026-06-24_030000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(snaps, "2026-05-01_010000"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a non-snapshot dir and a file must be ignored
	_ = os.MkdirAll(filepath.Join(snaps, "notatimestamp"), 0o755)
	_ = os.WriteFile(filepath.Join(snaps, "x.txt"), []byte("y"), 0o644)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %v", len(got), got)
	}
}

func TestListMissingDirReturnsEmpty(t *testing.T) {
	got, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("missing snapshots dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: 编译失败（未定义 List）。

- [ ] **Step 3: 写实现（接口/常量/List/FSType；Detect 留到 Task 9）**

Create `gsync/internal/snapshot/snapshot.go`:
```go
// Package snapshot abstracts snapshot creation over hardlink and btrfs backends.
package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// TSLayout is the timestamp format used for snapshot directory names.
const TSLayout = "2006-01-02_150405"

// BtrfsMagic is the statfs f_type for a btrfs filesystem.
const BtrfsMagic int64 = 0x9123683E

// FSTypeFunc returns the filesystem magic number for a path.
type FSTypeFunc func(path string) (int64, error)

// RealFSType returns the statfs f_type for path (Linux).
func RealFSType(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

// Backend creates and manages snapshots under a local root directory.
type Backend interface {
	Name() string
	EnsureCurrent(ctx context.Context, root string) (currentPath string, err error)
	Create(ctx context.Context, root string, ts time.Time) (snapPath string, err error)
	Delete(ctx context.Context, snapPath string) error
	List(root string) ([]time.Time, error)
}

// List parses the timestamps of existing snapshot directories under
// root/snapshots. A missing directory yields an empty slice, not an error.
func List(root string) ([]time.Time, error) {
	dir := filepath.Join(root, "snapshots")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := time.Parse(TSLayout, e.Name())
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// Detect (backend selection) is added in Task 9, once both backends exist; it
// is the only thing in this package that imports execx.
```

> 注：本任务的 `snapshot.go` 不 import `execx`（`Backend` 接口与 `List` 都不需要）。`Detect` 及其对 `execx` 的依赖在 Task 9 加入。

- [ ] **Step 4: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: PASS（List 两个用例通过）。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/snapshot/snapshot.go internal/snapshot/snapshot_test.go && git commit -m "feat: snapshot 后端接口、常量与共享 List"
```

---

### Task 8: hardlink 后端

**Files:**
- Create: `gsync/internal/snapshot/hardlink.go`
- Test: `gsync/internal/snapshot/hardlink_test.go`

**Interfaces:**
- Consumes: `execx.Runner`、`Backend`、`List`、`TSLayout`（Task 7）
- Produces: `func NewHardlink(r execx.Runner) Backend`；`type Hardlink`（实现 Backend，`Name()=="hardlink"`，Create 调用 `cp -al`）

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/snapshot/hardlink_test.go`:
```go
package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/execx"
)

func TestHardlinkEnsureCurrentCreatesDir(t *testing.T) {
	root := t.TempDir()
	be := NewHardlink(&execx.FakeRunner{})
	cur, err := be.EnsureCurrent(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if cur != filepath.Join(root, "current") {
		t.Fatalf("cur = %q", cur)
	}
	if fi, err := os.Stat(cur); err != nil || !fi.IsDir() {
		t.Fatalf("current dir not created: %v", err)
	}
}

func TestHardlinkCreateInvokesCpAl(t *testing.T) {
	root := t.TempDir()
	fr := &execx.FakeRunner{}
	be := NewHardlink(fr)
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if snap != want {
		t.Fatalf("snap = %q, want %q", snap, want)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Name != "cp" {
		t.Fatalf("expected cp call, got %+v", fr.Calls)
	}
	args := fr.Calls[0].Args
	if args[0] != "-al" || args[1] != filepath.Join(root, "current") || args[2] != want {
		t.Fatalf("cp args = %v", args)
	}
	if fi, err := os.Stat(filepath.Join(root, "snapshots")); err != nil || !fi.IsDir() {
		t.Fatalf("snapshots dir not created: %v", err)
	}
}

func TestHardlinkNameAndDelete(t *testing.T) {
	be := NewHardlink(&execx.FakeRunner{})
	if be.Name() != "hardlink" {
		t.Fatalf("name = %q", be.Name())
	}
	root := t.TempDir()
	target := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := be.Delete(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("delete did not remove snapshot")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: 编译失败（未定义 NewHardlink）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/snapshot/hardlink.go`:
```go
package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/execx"
)

// Hardlink implements Backend using `cp -al` hardlink copies. It works on any
// POSIX filesystem and needs no privileges.
type Hardlink struct {
	r execx.Runner
}

// NewHardlink returns a hardlink-backed Backend.
func NewHardlink(r execx.Runner) Backend { return &Hardlink{r: r} }

// Name implements Backend.
func (*Hardlink) Name() string { return "hardlink" }

// EnsureCurrent creates root/current as a plain directory.
func (h *Hardlink) EnsureCurrent(_ context.Context, root string) (string, error) {
	cur := filepath.Join(root, "current")
	return cur, os.MkdirAll(cur, 0o755)
}

// Create hardlink-copies root/current into root/snapshots/<ts>.
func (h *Hardlink) Create(ctx context.Context, root string, ts time.Time) (string, error) {
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(snaps, ts.Format(TSLayout))
	cur := filepath.Join(root, "current")
	if _, err := h.r.Run(ctx, "cp", "-al", cur, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Delete removes a snapshot directory tree.
func (h *Hardlink) Delete(_ context.Context, snapPath string) error {
	return os.RemoveAll(snapPath)
}

// List implements Backend.
func (h *Hardlink) List(root string) ([]time.Time, error) { return List(root) }
```

- [ ] **Step 4: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: PASS（Task 7 + Task 8 用例）。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/snapshot/hardlink.go internal/snapshot/hardlink_test.go && git commit -m "feat: hardlink 快照后端"
```

---

### Task 9: btrfs 后端与 Detect 接线

**Files:**
- Create: `gsync/internal/snapshot/btrfs.go`
- Test: `gsync/internal/snapshot/btrfs_test.go`
- Modify: `gsync/internal/snapshot/snapshot.go`（删除占位行，新增 `Detect`）

**Interfaces:**
- Consumes: `execx.Runner`、`Backend`、`FSTypeFunc`、`BtrfsMagic`、`NewHardlink`（Task 8）
- Produces:
  - `func NewBtrfs(r execx.Runner) Backend`；`type Btrfs`（实现 Backend，`Name()=="btrfs"`）
  - `var ErrCurrentNotSubvolume = errors.New(...)`
  - `func Detect(ctx context.Context, root string, r execx.Runner, fsType FSTypeFunc) Backend`

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/snapshot/btrfs_test.go`:
```go
package snapshot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/execx"
)

func TestBtrfsCreateInvokesSubvolumeSnapshot(t *testing.T) {
	root := t.TempDir()
	fr := &execx.FakeRunner{}
	be := NewBtrfs(fr)
	ts := time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC)
	snap, err := be.Create(context.Background(), root, ts)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "snapshots", "2026-06-24_030000")
	if snap != want {
		t.Fatalf("snap = %q", snap)
	}
	c := fr.Calls[0]
	if c.Name != "btrfs" || strings.Join(c.Args, " ") != "subvolume snapshot -r "+
		filepath.Join(root, "current")+" "+want {
		t.Fatalf("btrfs args = %v", c.Args)
	}
}

func TestBtrfsEnsureCurrentNotSubvolume(t *testing.T) {
	root := t.TempDir()
	// pre-create current as a plain dir
	be := NewHardlink(&execx.FakeRunner{})
	if _, err := be.EnsureCurrent(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		// `btrfs subvolume show` fails -> not a subvolume
		return execx.Result{Code: 1}, errors.New("not a subvolume")
	}}
	bb := NewBtrfs(fr)
	if _, err := bb.EnsureCurrent(context.Background(), root); !errors.Is(err, ErrCurrentNotSubvolume) {
		t.Fatalf("err = %v, want ErrCurrentNotSubvolume", err)
	}
}

func TestDetectChoosesBackend(t *testing.T) {
	ctx := context.Background()
	okBtrfs := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{}, nil // `btrfs --version` succeeds
	}}
	btrfsFS := func(string) (int64, error) { return BtrfsMagic, nil }
	if be := Detect(ctx, "/x", okBtrfs, btrfsFS); be.Name() != "btrfs" {
		t.Fatalf("want btrfs, got %s", be.Name())
	}
	ext4FS := func(string) (int64, error) { return 0xEF53, nil }
	if be := Detect(ctx, "/x", okBtrfs, ext4FS); be.Name() != "hardlink" {
		t.Fatalf("want hardlink on ext4, got %s", be.Name())
	}
	// btrfs FS but no btrfs binary -> hardlink
	noBin := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		return execx.Result{Code: 127}, errors.New("not found")
	}}
	if be := Detect(ctx, "/x", noBin, btrfsFS); be.Name() != "hardlink" {
		t.Fatalf("want hardlink when btrfs missing, got %s", be.Name())
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: 编译失败（未定义 NewBtrfs/Detect/ErrCurrentNotSubvolume）。

- [ ] **Step 3: 写 btrfs.go**

Create `gsync/internal/snapshot/btrfs.go`:
```go
package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/execx"
)

// ErrCurrentNotSubvolume means root/current exists but is not a btrfs subvolume,
// so native snapshots are impossible and the caller should fall back.
var ErrCurrentNotSubvolume = errors.New("current exists but is not a btrfs subvolume")

// Btrfs implements Backend using native read-only subvolume snapshots.
type Btrfs struct {
	r execx.Runner
}

// NewBtrfs returns a btrfs-backed Backend.
func NewBtrfs(r execx.Runner) Backend { return &Btrfs{r: r} }

// Name implements Backend.
func (*Btrfs) Name() string { return "btrfs" }

// EnsureCurrent creates root/current as a subvolume, or verifies an existing one.
func (b *Btrfs) EnsureCurrent(ctx context.Context, root string) (string, error) {
	cur := filepath.Join(root, "current")
	_, statErr := os.Stat(cur)
	if errors.Is(statErr, fs.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
		if _, err := b.r.Run(ctx, "btrfs", "subvolume", "create", cur); err != nil {
			return "", err
		}
		return cur, nil
	}
	if statErr != nil {
		return "", statErr
	}
	if _, err := b.r.Run(ctx, "btrfs", "subvolume", "show", cur); err != nil {
		return "", ErrCurrentNotSubvolume
	}
	return cur, nil
}

// Create makes a read-only subvolume snapshot.
func (b *Btrfs) Create(ctx context.Context, root string, ts time.Time) (string, error) {
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(snaps, ts.Format(TSLayout))
	cur := filepath.Join(root, "current")
	if _, err := b.r.Run(ctx, "btrfs", "subvolume", "snapshot", "-r", cur, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Delete removes a snapshot subvolume.
func (b *Btrfs) Delete(ctx context.Context, snapPath string) error {
	_, err := b.r.Run(ctx, "btrfs", "subvolume", "delete", snapPath)
	return err
}

// List implements Backend.
func (b *Btrfs) List(root string) ([]time.Time, error) { return List(root) }
```

- [ ] **Step 4: 在 snapshot.go 接线 Detect（新增 execx import + Detect）**

In `gsync/internal/snapshot/snapshot.go`, add the execx import to the import block:
```go
	"syscall"
	"time"

	"gsync/internal/execx"
)
```
and replace the trailing placeholder comment:
```go
// Detect (backend selection) is added in Task 9, once both backends exist; it
// is the only thing in this package that imports execx.
```
with the Detect function:
```go
// Detect chooses btrfs when root is on a btrfs filesystem and the `btrfs`
// command is available; otherwise it returns the hardlink backend.
func Detect(ctx context.Context, root string, r execx.Runner, fsType FSTypeFunc) Backend {
	if magic, err := fsType(root); err == nil && magic == BtrfsMagic {
		if _, err := r.Run(ctx, "btrfs", "--version"); err == nil {
			return NewBtrfs(r)
		}
	}
	return NewHardlink(r)
}
```

- [ ] **Step 5: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/snapshot/`
Expected: PASS（Task 7+8+9 全部用例）。

- [ ] **Step 6: Commit**

```bash
cd /home/user/work/gsync && git add internal/snapshot && git commit -m "feat: btrfs 快照后端与后端探测"
```

---

### Task 10: syncer 辅助函数（stats / ssh / rsync 参数）

**Files:**
- Create: `gsync/internal/syncer/helpers.go`
- Test: `gsync/internal/syncer/helpers_test.go`

**Interfaces:**
- Consumes: `config.Sync`、`config.ExpandHome`、`ignore.ToRsyncFilters`
- Produces（包内，首字母小写，供 Task 11 调用）：
  - `func parseStats(out string) (files, bytes int64)`
  - `func sshOptArg(identity string, port int, strict bool) string`（rsync `-e` 的单串值）
  - `func sshCmdArgs(identity string, port int, strict bool, user, host, remoteCmd string) []string`（直接 ssh 预检参数）
  - `func buildRsyncArgs(s config.Sync, port int, currentPath string, dryRun bool) []string`
  - `func installHint() string`
  - `func ensureTrailingSlash(p string) string`

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/syncer/helpers_test.go`:
```go
package syncer

import (
	"strings"
	"testing"

	"gsync/internal/config"
)

func TestParseStats(t *testing.T) {
	out := `
Number of files: 100
Number of regular files transferred: 12
Total file size: 999 bytes
Total transferred file size: 3456 bytes
`
	files, bytes := parseStats(out)
	if files != 12 || bytes != 3456 {
		t.Fatalf("files=%d bytes=%d", files, bytes)
	}
}

func TestSSHOptArg(t *testing.T) {
	got := sshOptArg("/k", 2222, false)
	if !strings.Contains(got, "ssh -p 2222") ||
		!strings.Contains(got, "-i /k") ||
		!strings.Contains(got, "BatchMode=yes") ||
		!strings.Contains(got, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("opt arg = %q", got)
	}
	strict := sshOptArg("/k", 22, true)
	if !strings.Contains(strict, "StrictHostKeyChecking=yes") {
		t.Fatalf("strict opt = %q", strict)
	}
}

func TestSSHCmdArgs(t *testing.T) {
	args := sshCmdArgs("/k", 22, false, "u", "h", "command -v rsync")
	j := strings.Join(args, " ")
	if !strings.Contains(j, "-p 22") || !strings.Contains(j, "-i /k") ||
		!strings.HasSuffix(j, "u@h command -v rsync") {
		t.Fatalf("cmd args = %v", args)
	}
}

func TestBuildRsyncArgs(t *testing.T) {
	s := config.Sync{
		User: "u", Host: "h", RemotePath: "/src", Ignore: []string{"*.log"},
	}
	args := buildRsyncArgs(s, 22, "/local/current", false)
	j := strings.Join(args, " ")
	if !strings.Contains(j, "-a") || !strings.Contains(j, "--delete") ||
		!strings.Contains(j, "--info=stats2") {
		t.Fatalf("missing base flags: %v", args)
	}
	if !strings.Contains(j, "--filter - *.log") {
		t.Fatalf("missing filter: %v", args)
	}
	if !strings.Contains(j, "u@h:/src/") || !strings.Contains(j, "/local/current/") {
		t.Fatalf("missing src/dst with trailing slash: %v", args)
	}
	if strings.Contains(j, " -n") {
		t.Fatalf("dry-run should be off: %v", args)
	}
	dry := buildRsyncArgs(s, 22, "/local/current", true)
	if !contains(dry, "-n") {
		t.Fatalf("dry-run flag missing: %v", dry)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/`
Expected: 编译失败（未定义 helpers）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/syncer/helpers.go`:
```go
package syncer

import (
	"fmt"
	"strconv"
	"strings"

	"gsync/internal/config"
	"gsync/internal/ignore"
)

// parseStats extracts the transferred file count and byte count from rsync's
// --info=stats2 / --stats output.
func parseStats(out string) (files, bytes int64) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "regular files transferred:"),
			strings.Contains(line, "files transferred:"):
			files = lastInt(line)
		case strings.Contains(line, "Total transferred file size:"):
			bytes = lastInt(line)
		}
	}
	return files, bytes
}

// lastInt returns the last run of digits in a line as an int64 (0 if none).
func lastInt(line string) int64 {
	var digits strings.Builder
	var last string
	for _, r := range line {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else if digits.Len() > 0 {
			last = digits.String()
			digits.Reset()
		}
	}
	if digits.Len() > 0 {
		last = digits.String()
	}
	if last == "" {
		return 0
	}
	n, _ := strconv.ParseInt(last, 10, 64)
	return n
}

func strictOpt(strict bool) string {
	if strict {
		return "StrictHostKeyChecking=yes"
	}
	return "StrictHostKeyChecking=accept-new"
}

// sshOptArg builds the single string passed to rsync's -e option.
func sshOptArg(identity string, port int, strict bool) string {
	parts := []string{"ssh", "-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", strictOpt(strict)}
	if identity != "" {
		parts = append(parts, "-i", config.ExpandHome(identity))
	}
	return strings.Join(parts, " ")
}

// sshCmdArgs builds args for invoking ssh directly (used by preflight).
func sshCmdArgs(identity string, port int, strict bool, user, host, remoteCmd string) []string {
	args := []string{"-p", strconv.Itoa(port), "-o", "BatchMode=yes", "-o", strictOpt(strict)}
	if identity != "" {
		args = append(args, "-i", config.ExpandHome(identity))
	}
	args = append(args, fmt.Sprintf("%s@%s", user, host), remoteCmd)
	return args
}

// ensureTrailingSlash guarantees a trailing slash (rsync dir-content semantics).
func ensureTrailingSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// buildRsyncArgs assembles the full rsync argument list for one entry.
func buildRsyncArgs(s config.Sync, port int, currentPath string, dryRun bool) []string {
	args := []string{"-a", "--delete", "--info=stats2"}
	if dryRun {
		args = append(args, "-n")
	}
	for _, f := range ignore.ToRsyncFilters(s.Ignore) {
		args = append(args, "--filter", f)
	}
	args = append(args, "-e", sshOptArg(s.Identity, port, s.StrictHostKey))
	src := fmt.Sprintf("%s@%s:%s", s.User, s.Host, ensureTrailingSlash(s.RemotePath))
	args = append(args, src, ensureTrailingSlash(currentPath))
	return args
}

// installHint returns a multi-package-manager hint for installing rsync.
func installHint() string {
	return "install rsync, e.g.: apt install rsync | dnf install rsync | " +
		"yum install rsync | apk add rsync | pacman -S rsync"
}
```

- [ ] **Step 4: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/syncer/helpers.go internal/syncer/helpers_test.go && git commit -m "feat: syncer 辅助函数（stats/ssh/rsync 参数）"
```

---

### Task 11: syncer 编排 SyncOne/SyncMany

**Files:**
- Create: `gsync/internal/syncer/syncer.go`
- Test: `gsync/internal/syncer/syncer_test.go`

**Interfaces:**
- Consumes: `execx.Runner`、`config.Sync/Defaults`、`snapshot.*`、`retention.*`、helpers（Task 10）
- Produces:
  - `type Logger interface { Infof(string, ...any); Errorf(string, ...any) }`
  - `type Deps struct { Runner execx.Runner; FSType snapshot.FSTypeFunc; Log Logger; Now func() time.Time }`
  - `type Result struct { Name string; OK bool; Err error; Files, Bytes int64; Snapshot, Mode string; Pruned int }`
  - `func SyncOne(ctx, s config.Sync, d config.Defaults, deps Deps, dryRun bool) Result`
  - `func SyncMany(ctx, entries []config.Sync, d config.Defaults, deps Deps, dryRun bool) []Result`

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/syncer/syncer_test.go`:
```go
package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
)

type captureLog struct{ lines []string }

func (c *captureLog) Infof(f string, a ...any)  { c.lines = append(c.lines, "I:"+sprintf(f, a)) }
func (c *captureLog) Errorf(f string, a ...any) { c.lines = append(c.lines, "E:"+sprintf(f, a)) }
func sprintf(f string, a []any) string         { return strings.TrimSpace(fmtSprintf(f, a)) }

func ext4FS(string) (int64, error) { return 0xEF53, nil }

func okEntry(t *testing.T) config.Sync {
	t.Helper()
	return config.Sync{
		Name: "web", Host: "h", User: "u", Identity: "",
		RemotePath: "/src", LocalPath: t.TempDir(),
	}
}

// happy path: rsync ok, hardlink snapshot created, prune keeps the only one.
func TestSyncOneHappyPath(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 && args[0] == "--version" {
			return execx.Result{Stdout: "rsync version 3"}, nil
		}
		if name == "ssh" {
			return execx.Result{Stdout: "/usr/bin/rsync"}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 5\nTotal transferred file size: 42 bytes\n"}, nil
		}
		if name == "cp" {
			// emulate cp -al by creating the dir so List() sees it
			_ = os.MkdirAll(args[2], 0o755)
			return execx.Result{}, nil
		}
		return execx.Result{}, nil
	}}
	d := config.Defaults{Retention: config.Retention{Recent: 5}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	res := SyncOne(context.Background(), s, d, deps, false)
	if !res.OK || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Files != 5 || res.Bytes != 42 {
		t.Fatalf("stats = %+v", res)
	}
	if res.Mode != "hardlink" {
		t.Fatalf("mode = %q", res.Mode)
	}
	if _, err := os.Stat(filepath.Join(s.LocalPath, "snapshots", "2026-06-24_030000")); err != nil {
		t.Fatalf("snapshot not created: %v", err)
	}
}

// rsync missing locally -> fail, no snapshot.
func TestSyncOneLocalRsyncMissing(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Code: 127}, errors.New("not found")
		}
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, false)
	if res.OK || res.Err == nil {
		t.Fatalf("expected failure, got %+v", res)
	}
}

// dry-run: no snapshot creation.
func TestSyncOneDryRun(t *testing.T) {
	s := okEntry(t)
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			t.Fatal("dry-run must not snapshot")
		}
		return execx.Result{}, nil
	}}
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := SyncOne(context.Background(), s, config.Defaults{}, deps, true)
	if !res.OK {
		t.Fatalf("dry-run res = %+v", res)
	}
}

func TestSyncManyIsolatesFailures(t *testing.T) {
	good := okEntry(t)
	good.Name = "good"
	bad := okEntry(t)
	bad.Name = "bad"
	fr := &execx.FakeRunner{Handler: func(name string, args []string) (execx.Result, error) {
		if name == "rsync" && len(args) == 1 {
			return execx.Result{Stdout: "v"}, nil
		}
		if name == "ssh" {
			return execx.Result{}, nil
		}
		if name == "rsync" && containsStr(args, "h-bad:/src/") {
			return execx.Result{Code: 1}, errors.New("rsync failed")
		}
		if name == "rsync" {
			return execx.Result{Stdout: "Number of regular files transferred: 1\n"}, nil
		}
		if name == "cp" {
			_ = os.MkdirAll(args[2], 0o755)
		}
		return execx.Result{}, nil
	}}
	bad.Host = "h-bad"
	good.Host = "h-good"
	deps := Deps{Runner: fr, FSType: ext4FS, Log: &captureLog{},
		Now: func() time.Time { return time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC) }}
	results := SyncMany(context.Background(), []config.Sync{good, bad}, config.Defaults{}, deps, false)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	okCount := 0
	for _, r := range results {
		if r.OK {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("want exactly 1 ok, got %d: %+v", okCount, results)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
```

并在文件顶部补一个对 `fmt.Sprintf` 的薄封装（避免与测试里的局部 `sprintf` 命名冲突）。在 `syncer_test.go` 末尾追加：
```go
// fmtSprintf wraps fmt.Sprintf so the helper above stays terse.
func fmtSprintf(f string, a []any) string { return fmtSprintfImpl(f, a) }
```
并新建 `gsync/internal/syncer/testutil_test.go`:
```go
package syncer

import "fmt"

func fmtSprintfImpl(f string, a []any) string { return fmt.Sprintf(f, a...) }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/`
Expected: 编译失败（未定义 SyncOne/SyncMany/Deps/Result/Logger）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/syncer/syncer.go`:
```go
// Package syncer orchestrates the per-entry pipeline: preflight, rsync pull,
// snapshot, and retention prune.
package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/retention"
	"gsync/internal/snapshot"
)

// Logger is the subset of logging the syncer needs.
type Logger interface {
	Infof(format string, a ...any)
	Errorf(format string, a ...any)
}

// Deps are the injectable dependencies for syncing.
type Deps struct {
	Runner execx.Runner
	FSType snapshot.FSTypeFunc
	Log    Logger
	Now    func() time.Time
}

// Result is the outcome of syncing one entry.
type Result struct {
	Name     string
	OK       bool
	Err      error
	Files    int64
	Bytes    int64
	Snapshot string
	Mode     string
	Pruned   int
}

func toPolicy(r config.Retention) retention.Policy {
	return retention.Policy{
		Recent: r.Recent, Monthly: r.Monthly,
		Semiannual: r.Semiannual, Yearly: r.Yearly,
	}
}

// SyncOne runs the full pipeline for a single entry. It never panics; failures
// are reported via Result.Err with res.OK == false.
func SyncOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps, dryRun bool) Result {
	res := Result{Name: s.Name}
	port := s.EffectivePort(d)

	if _, err := deps.Runner.Run(ctx, "rsync", "--version"); err != nil {
		deps.Log.Errorf("[%s] local rsync missing: %s", s.Name, installHint())
		res.Err = err
		return res
	}
	if _, err := deps.Runner.Run(ctx, "ssh",
		sshCmdArgs(s.Identity, port, s.StrictHostKey, s.User, s.Host, "command -v rsync")...); err != nil {
		deps.Log.Errorf("[%s] remote rsync missing on %s: %s", s.Name, s.Host, installHint())
		res.Err = err
		return res
	}

	be := snapshot.Detect(ctx, s.LocalPath, deps.Runner, deps.FSType)
	cur, err := be.EnsureCurrent(ctx, s.LocalPath)
	if errors.Is(err, snapshot.ErrCurrentNotSubvolume) {
		deps.Log.Errorf("[%s] current is not a subvolume; falling back to hardlink", s.Name)
		be = snapshot.NewHardlink(deps.Runner)
		cur, err = be.EnsureCurrent(ctx, s.LocalPath)
	}
	if err != nil {
		deps.Log.Errorf("[%s] ensure current: %v", s.Name, err)
		res.Err = err
		return res
	}
	res.Mode = be.Name()
	deps.Log.Infof("[%s] snapshot mode: %s", s.Name, be.Name())

	out, err := deps.Runner.Run(ctx, "rsync", buildRsyncArgs(s, port, cur, dryRun)...)
	if err != nil {
		deps.Log.Errorf("[%s] rsync failed: %v: %s", s.Name, err, out.Stderr)
		res.Err = err
		return res
	}
	res.Files, res.Bytes = parseStats(out.Stdout)
	deps.Log.Infof("[%s] pulled %d files, %d bytes", s.Name, res.Files, res.Bytes)

	if dryRun {
		res.OK = true
		return res
	}

	ts := deps.Now()
	snap, err := be.Create(ctx, s.LocalPath, ts)
	if err != nil {
		deps.Log.Errorf("[%s] snapshot failed: %v", s.Name, err)
		res.Err = err
		return res
	}
	res.Snapshot = snap
	deps.Log.Infof("[%s] snapshot created: %s", s.Name, snap)

	times, err := be.List(s.LocalPath)
	if err != nil {
		deps.Log.Errorf("[%s] list snapshots: %v", s.Name, err)
		res.Err = err
		return res
	}
	_, del := retention.Partition(times, toPolicy(s.EffectiveRetention(d)))
	for _, t := range del {
		p := filepath.Join(s.LocalPath, "snapshots", t.Format(snapshot.TSLayout))
		if err := be.Delete(ctx, p); err != nil {
			deps.Log.Errorf("[%s] prune %s: %v", s.Name, p, err)
			continue
		}
		res.Pruned++
	}
	deps.Log.Infof("[%s] pruned %d snapshots", s.Name, res.Pruned)

	res.OK = true
	return res
}

// SyncMany runs entries sequentially, isolating per-entry failures.
func SyncMany(ctx context.Context, entries []config.Sync, d config.Defaults, deps Deps, dryRun bool) []Result {
	results := make([]Result, 0, len(entries))
	for _, s := range entries {
		results = append(results, SyncOne(ctx, s, d, deps, dryRun))
	}
	return results
}
```

- [ ] **Step 4: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/syncer && git commit -m "feat: syncer 编排 SyncOne/SyncMany"
```

---

### Task 12: syncer.PruneOne（仅清理）

**Files:**
- Create: `gsync/internal/syncer/prune.go`
- Test: `gsync/internal/syncer/prune_test.go`

**Interfaces:**
- Consumes: `snapshot.*`、`retention.*`、`Deps`、`Result`、`toPolicy`（Task 11）
- Produces: `func PruneOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) Result`

- [ ] **Step 1: 写失败测试**

Create `gsync/internal/syncer/prune_test.go`:
```go
package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
)

func TestPruneOneDeletesExcess(t *testing.T) {
	root := t.TempDir()
	snaps := filepath.Join(root, "snapshots")
	for _, n := range []string{"2026-06-24_030000", "2026-06-23_030000", "2026-06-22_030000"} {
		if err := os.MkdirAll(filepath.Join(snaps, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := config.Sync{Name: "web", LocalPath: root}
	d := config.Defaults{Retention: config.Retention{Recent: 1}} // keep only newest
	deps := Deps{Runner: &execx.FakeRunner{}, FSType: ext4FS, Log: &captureLog{}, Now: time.Now}
	res := PruneOne(context.Background(), s, d, deps)
	if !res.OK || res.Pruned != 2 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(snaps, "2026-06-24_030000")); err != nil {
		t.Fatal("newest should survive")
	}
	if _, err := os.Stat(filepath.Join(snaps, "2026-06-22_030000")); err == nil {
		t.Fatal("old snapshot should be pruned")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/ -run TestPruneOne`
Expected: 编译失败（未定义 PruneOne）。

- [ ] **Step 3: 写实现**

Create `gsync/internal/syncer/prune.go`:
```go
package syncer

import (
	"context"
	"path/filepath"

	"gsync/internal/config"
	"gsync/internal/retention"
	"gsync/internal/snapshot"
)

// PruneOne applies the retention policy to one entry without syncing.
func PruneOne(ctx context.Context, s config.Sync, d config.Defaults, deps Deps) Result {
	res := Result{Name: s.Name}
	be := snapshot.Detect(ctx, s.LocalPath, deps.Runner, deps.FSType)
	res.Mode = be.Name()
	times, err := be.List(s.LocalPath)
	if err != nil {
		deps.Log.Errorf("[%s] list snapshots: %v", s.Name, err)
		res.Err = err
		return res
	}
	_, del := retention.Partition(times, toPolicy(s.EffectiveRetention(d)))
	for _, t := range del {
		p := filepath.Join(s.LocalPath, "snapshots", t.Format(snapshot.TSLayout))
		if err := be.Delete(ctx, p); err != nil {
			deps.Log.Errorf("[%s] prune %s: %v", s.Name, p, err)
			continue
		}
		res.Pruned++
	}
	deps.Log.Infof("[%s] pruned %d snapshots", s.Name, res.Pruned)
	res.OK = true
	return res
}
```

- [ ] **Step 4: 运行测试**

Run: `cd /home/user/work/gsync && go test ./internal/syncer/`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
cd /home/user/work/gsync && git add internal/syncer/prune.go internal/syncer/prune_test.go && git commit -m "feat: 仅清理命令 PruneOne"
```

---

### Task 13: CLI 装配（main）

**Files:**
- Create: `gsync/cli.go`
- Test: `gsync/cli_test.go`
- Modify: `gsync/main.go`（替换为完整子命令分发）

**Interfaces:**
- Consumes: `config.*`、`syncer.*`、`snapshot.*`、`logx.*`、`execx.Real`
- Produces（package main，可测试纯函数）：
  - `func resolveConfigPath(flag, exeDir string) string`
  - `func selectEntries(all []config.Sync, name, server string) []config.Sync`
  - `func summaryLine(results []syncer.Result, dur time.Duration) string`

- [ ] **Step 1: 写失败测试**

Create `gsync/cli_test.go`:
```go
package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gsync/internal/config"
	"gsync/internal/syncer"
)

func TestResolveConfigPath(t *testing.T) {
	if got := resolveConfigPath("", "/opt/app"); got != "/opt/app/config.toml" {
		t.Fatalf("got %q", got)
	}
	if got := resolveConfigPath("/custom.toml", "/opt/app"); got != "/custom.toml" {
		t.Fatalf("got %q", got)
	}
}

func TestSelectEntries(t *testing.T) {
	all := []config.Sync{
		{Name: "a", Host: "h1"},
		{Name: "b", Host: "h2"},
		{Name: "c", Host: "h1"},
	}
	if got := selectEntries(all, "", ""); len(got) != 3 {
		t.Fatalf("all: %d", len(got))
	}
	if got := selectEntries(all, "b", ""); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("by name: %v", got)
	}
	if got := selectEntries(all, "", "h1"); len(got) != 2 {
		t.Fatalf("by server: %v", got)
	}
}

func TestSummaryLine(t *testing.T) {
	res := []syncer.Result{
		{Name: "a", OK: true},
		{Name: "b", OK: false, Err: errors.New("x")},
	}
	got := summaryLine(res, 3*time.Second)
	if !strings.Contains(got, "成功 1") || !strings.Contains(got, "失败 1") ||
		!strings.Contains(got, "3") {
		t.Fatalf("summary = %q", got)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /home/user/work/gsync && go test .`
Expected: 编译失败（未定义 resolveConfigPath 等）。

- [ ] **Step 3: 写 cli.go**

Create `gsync/cli.go`:
```go
package main

import (
	"fmt"
	"path/filepath"
	"time"

	"gsync/internal/config"
	"gsync/internal/syncer"
)

// resolveConfigPath returns the explicit flag value or exeDir/config.toml.
func resolveConfigPath(flag, exeDir string) string {
	if flag != "" {
		return flag
	}
	return filepath.Join(exeDir, "config.toml")
}

// selectEntries filters by entry name and/or server host. Empty filters match all.
func selectEntries(all []config.Sync, name, server string) []config.Sync {
	var out []config.Sync
	for _, s := range all {
		if name != "" && s.Name != name {
			continue
		}
		if server != "" && s.Host != server {
			continue
		}
		out = append(out, s)
	}
	return out
}

// summaryLine formats the one-line run summary.
func summaryLine(results []syncer.Result, dur time.Duration) string {
	ok, fail := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	return fmt.Sprintf("成功 %d / 失败 %d / 耗时 %.1fs", ok, fail, dur.Seconds())
}
```

- [ ] **Step 4: 运行 cli 测试确认通过**

Run: `cd /home/user/work/gsync && go test .`
Expected: PASS。

- [ ] **Step 5: 写完整 main.go**

Replace `gsync/main.go` with:
```go
// Command gsync pulls remote folders over ssh+rsync and keeps GFS snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/logx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
)

const version = "0.1.0"

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "TUI 尚未实现（见后续计划）。可用子命令: sync | list | snapshots | prune | version")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("gsync", version)
	case "sync":
		os.Exit(cmdSync(os.Args[2:]))
	case "list":
		os.Exit(cmdList(os.Args[2:]))
	case "snapshots":
		os.Exit(cmdSnapshots(os.Args[2:]))
	case "prune":
		os.Exit(cmdPrune(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func loadConfig(cfgFlag string) (*config.Config, error) {
	return config.Load(resolveConfigPath(cfgFlag, exeDir()))
}

func realDeps(log syncer.Logger) syncer.Deps {
	return syncer.Deps{
		Runner: execx.Real{},
		FSType: snapshot.RealFSType,
		Log:    log,
		Now:    time.Now,
	}
}

func cmdSync(argv []string) int {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "only this entry")
	server := fs.String("server", "", "only entries on this host")
	dry := fs.Bool("dry-run", false, "rsync -n, no snapshot")
	_ = fs.Parse(argv)

	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	logDir := filepath.Join(exeDir(), "logs")
	start := time.Now()
	rl, err := logx.NewRunLogger(logDir, start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		return 1
	}
	defer rl.Close()

	entries := selectEntries(cfg.Sync, *name, *server)
	results := syncer.SyncMany(context.Background(), entries, cfg.Defaults, realDeps(rl), *dry)

	line := summaryLine(results, time.Since(start))
	rl.Infof("%s", line)
	_ = logx.AppendSummary(logDir, start.Format("2006-01-02 15:04:05")+" "+line)
	_ = logx.Cleanup(logDir, cfg.Log.KeepDays, cfg.Log.KeepCount, time.Now())
	fmt.Println(line)

	for _, r := range results {
		if !r.OK {
			return 1
		}
	}
	return 0
}

func cmdList(argv []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	_ = fs.Parse(argv)
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	for _, s := range cfg.Sync {
		fmt.Printf("%-20s %s@%s:%s -> %s\n", s.Name, s.User, s.Host, s.RemotePath, s.LocalPath)
	}
	return 0
}

func cmdSnapshots(argv []string) int {
	fs := flag.NewFlagSet("snapshots", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "entry name (required)")
	_ = fs.Parse(argv)
	if *name == "" {
		fmt.Fprintln(os.Stderr, "snapshots: --name is required")
		return 2
	}
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	entries := selectEntries(cfg.Sync, *name, "")
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no entry named %q\n", *name)
		return 1
	}
	times, err := snapshot.List(entries[0].LocalPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}
	for _, t := range times {
		fmt.Println(t.Format(snapshot.TSLayout))
	}
	return 0
}

func cmdPrune(argv []string) int {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file path")
	name := fs.String("name", "", "only this entry")
	_ = fs.Parse(argv)
	cfg, err := loadConfig(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	logDir := filepath.Join(exeDir(), "logs")
	start := time.Now()
	rl, err := logx.NewRunLogger(logDir, start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		return 1
	}
	defer rl.Close()

	entries := selectEntries(cfg.Sync, *name, "")
	fail := false
	for _, s := range entries {
		r := syncer.PruneOne(context.Background(), s, cfg.Defaults, realDeps(rl))
		fmt.Printf("%s: pruned %d (mode %s)\n", r.Name, r.Pruned, r.Mode)
		if !r.OK {
			fail = true
		}
	}
	if fail {
		return 1
	}
	return 0
}
```

- [ ] **Step 6: 构建并冒烟测试**

Run:
```bash
cd /home/user/work/gsync && CGO_ENABLED=0 go build -o gsync . && go test ./... && ./gsync version
```
Expected: 构建成功；所有测试 PASS；输出 `gsync 0.1.0`。

- [ ] **Step 7: 端到端冒烟（list 命令 + 临时配置）**

Run:
```bash
cd /home/user/work/gsync && cat > /tmp/gsync-smoke.toml <<'EOF'
[[sync]]
name = "demo"
host = "example.com"
user = "u"
remote_path = "/srv/data/"
local_path = "/tmp/gsync-demo"
EOF
./gsync list --config /tmp/gsync-smoke.toml
```
Expected: 输出一行 `demo  u@example.com:/srv/data/ -> /tmp/gsync-demo`，退出码 0。

- [ ] **Step 8: Commit**

```bash
cd /home/user/work/gsync && git add main.go cli.go cli_test.go && git commit -m "feat: CLI 子命令分发（sync/list/snapshots/prune）"
```

---

## Self-Review（计划作者已执行）

**1. 规格覆盖**

| 规格条目 | 任务 |
|----------|------|
| §3 配置类型/Load/Save/Validate/Effective | Task 5 |
| §4 预检（rsync 检测+提示） | Task 11（SyncOne 步骤 1-2）、Task 10（installHint） |
| §4 rsync 拉取（-a --delete --info=stats2 --filter -e） | Task 10（buildRsyncArgs）、Task 11 |
| §4 后端探测 + current 创建 + 回退 | Task 7/8/9（Detect）、Task 11（ErrCurrentNotSubvolume 回退） |
| §4 快照创建（hardlink/btrfs） | Task 8/9 |
| §5 GFS 四层保留 | Task 3、Task 11（接线）、Task 12（prune 命令） |
| §6 CLI sync/list/snapshots/prune + 退出码 | Task 13 |
| §7 日志 per-run/summary/cleanup | Task 6、Task 13（接线） |
| §8 ignore 翻译 | Task 4 |
| §8 SSH BatchMode/strict_host_key | Task 10（sshOptArg/sshCmdArgs） |
| §8 单向拉取 | Task 10/11 |
| §1 单文件静态构建 | Task 1（CGO_ENABLED=0）、Task 13 Step 6 |

**2. 占位符扫描**：无 TBD/TODO；每个代码步骤含完整代码。

**3. 类型一致性**：`Backend` 接口方法、`Deps`/`Result` 字段、`TSLayout`、`Detect`/`NewHardlink`/`NewBtrfs`/`ErrCurrentNotSubvolume`、`SyncOne`/`SyncMany`/`PruneOne`、`toPolicy` 在定义任务与消费任务间签名一致。

**已知缺口（有意，非本计划范围）**：
- TUI（规格 §6）→ 见下「后续计划」。
- `current` 已是 btrfs 上普通目录时仅回退 hardlink 并告警（规格 §4 即如此约定）。
- ignore 翻译不处理「父目录被排除则无法重纳子文件」的 gitignore 边界（规格 §8 已声明为文档说明项）。

## 后续计划（不在本计划内）

实现并验证核心 CLI 后，另起一份计划 `2026-06-24-gsync-tui.md` 实现 bubbletea TUI：主菜单/条目编辑表单/手动运行/快照浏览，并在界面显著显示生效快照后端。TUI 复用本计划的 config/syncer/snapshot/logx，不改其接口。
