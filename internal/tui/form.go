package tui

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gsyncer/internal/config"
)

// text input indices.
const (
	fName = iota
	fHost
	fPort
	fUser
	fIdentity
	fRemote
	fLocal
	numInputs
)

// focus slots: inputs (0..numInputs-1), strict toggle, ignore textarea, ret[0..3].
const (
	focusStrict = numInputs
	focusIgnore = numInputs + 1
	focusRet0   = numInputs + 2
	numFocus    = focusRet0 + 4
	// focusPaste is an extra slot only present for new entries (origIdx < 0).
	focusPaste = numFocus
)

type formModel struct {
	cfg     *config.Config
	cfgPath string
	origIdx int // -1 == new

	width, height int

	inputs []textinput.Model // numInputs
	ignore textarea.Model
	ret    []textinput.Model // 4: recent, monthly, semiannual, yearly
	paste  textinput.Model   // quick-entry parser (new entries only)
	strict bool
	focus  int

	initial   map[string]string // snapshot for dirty detection
	status    string
	statusErr bool // status is an error (red) rather than informational (green)

	// discard-confirm state
	confirming bool
	exitQuit   bool // true: ctrl+c (quit); false: esc (back to list)
}

func newForm(cfg *config.Config, cfgPath string, origIdx int) formModel {
	labels := []string{"名称", "主机", "端口", "用户", "密钥", "远程路径", "本地路径"}
	m := formModel{cfg: cfg, cfgPath: cfgPath, origIdx: origIdx, focus: 0}
	m.inputs = make([]textinput.Model, numInputs)
	for i := range m.inputs {
		ti := textinput.New()
		ti.Placeholder = labels[i]
		m.inputs[i] = ti
	}
	m.ignore = textarea.New()
	m.ignore.SetWidth(40)
	m.ignore.SetHeight(6)
	m.ret = make([]textinput.Model, 4)
	// An empty field inherits defaults.retention (see EffectiveRetention), so
	// surface those numbers as placeholders instead of a blank box.
	def := cfg.Defaults.Retention
	for i, n := range []int{def.Recent, def.Monthly, def.Semiannual, def.Yearly} {
		ti := textinput.New()
		ti.CharLimit = 6
		ti.Placeholder = strconv.Itoa(n)
		m.ret[i] = ti
	}
	m.paste = textinput.New()
	m.paste.Placeholder = "user@host:/远程路径  或  host=.. user=.. remote=.. local=.."

	if origIdx >= 0 && origIdx < len(cfg.Sync) {
		s := cfg.Sync[origIdx]
		fillInputs(&m, s)
	} else {
		// new entry: seed sensible defaults so not every field is manual.
		m.inputs[fPort].SetValue(strconv.Itoa(defaultPort(cfg.Defaults)))
		m.ignore.SetValue(strings.Join(defaultIgnore, "\n"))
		r := defaultRetention(cfg.Defaults)
		m.ret[0].SetValue(strconv.Itoa(r.Recent))
		m.ret[1].SetValue(strconv.Itoa(r.Monthly))
		m.ret[2].SetValue(strconv.Itoa(r.Semiannual))
		m.ret[3].SetValue(strconv.Itoa(r.Yearly))
		// focus the quick-paste field first; the paste workflow is the fast path.
		m.focus = focusPaste
	}
	m.applyFocus()
	m.initial = m.snapshot()
	return m
}

// fillInputs populates the form controls from an existing sync entry.
func fillInputs(m *formModel, s config.Sync) {
	m.inputs[fName].SetValue(s.Name)
	m.inputs[fHost].SetValue(s.Host)
	if s.Port != 0 {
		m.inputs[fPort].SetValue(strconv.Itoa(s.Port))
	}
	m.inputs[fUser].SetValue(s.User)
	m.inputs[fIdentity].SetValue(s.Identity)
	m.inputs[fRemote].SetValue(s.RemotePath)
	m.inputs[fLocal].SetValue(s.LocalPath)
	m.strict = s.StrictHostKey
	m.ignore.SetValue(strings.Join(s.Ignore, "\n"))
	if s.Retention != nil {
		setIntPtr(&m.ret[0], s.Retention.Recent)
		setIntPtr(&m.ret[1], s.Retention.Monthly)
		setIntPtr(&m.ret[2], s.Retention.Semiannual)
		setIntPtr(&m.ret[3], s.Retention.Yearly)
	}
}

// defaultPort resolves the SSH port to pre-fill for a new entry.
func defaultPort(d config.Defaults) int {
	if d.SSHPort != 0 {
		return d.SSHPort
	}
	return 22
}

// defaultRetention resolves the retention values to pre-fill for a new entry:
// the configured defaults, or a sensible GFS fallback when none are set.
func defaultRetention(d config.Defaults) config.Retention {
	if d.Retention != (config.Retention{}) {
		return d.Retention
	}
	return config.Retention{Recent: 7, Monthly: 6, Semiannual: 2, Yearly: 2}
}

// defaultIgnore are the gitignore-style patterns pre-filled for a new entry,
// covering the most common build/cache/VCS noise.
var defaultIgnore = []string{
	"__pycache__/",
	"*.pyc",
	"node_modules/",
	".git/",
	".venv/",
	".DS_Store",
}

// newFormCopy builds a new (unsaved) entry pre-filled from an existing one,
// giving it a unique name so it can be saved alongside the original.
func newFormCopy(cfg *config.Config, cfgPath string, srcIdx int) formModel {
	m := newForm(cfg, cfgPath, srcIdx) // prefill from the source entry
	m.origIdx = -1                     // but persist as a brand-new entry
	if srcIdx >= 0 && srcIdx < len(cfg.Sync) {
		m.inputs[fName].SetValue(uniqueName(cfg, cfg.Sync[srcIdx].Name+"-copy"))
	}
	m.focus = 0 // land on 名称 so the user can review/rename
	m.applyFocus()
	// Compare against a blank new form so the populated copy reads as dirty
	// (esc then warns before discarding it).
	blank := newForm(cfg, cfgPath, -1)
	m.initial = blank.snapshot()
	return m
}

// uniqueName returns base, or base with a numeric suffix, not used by any entry.
func uniqueName(cfg *config.Config, base string) string {
	used := map[string]bool{}
	for _, s := range cfg.Sync {
		used[s.Name] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s%d", base, i)
		if !used[cand] {
			return cand
		}
	}
}

// focusCount is the number of focusable slots; the paste slot exists only for
// new entries.
func (m formModel) focusCount() int {
	if m.origIdx < 0 {
		return numFocus + 1
	}
	return numFocus
}

// parsePaste parses a quick-entry string into form field values, keyed by input
// index. Two forms are accepted:
//
//  1. key=value tokens:  name=foo host=1.2.3.4 port=22 user=root \
//     identity=~/.ssh/id remote=/data local=~/data
//  2. scp shorthand:     [user@]host:/remote/path
func parsePaste(s string) map[int]string {
	s = strings.TrimSpace(s)
	out := map[int]string{}
	if s == "" {
		return out
	}
	if strings.Contains(s, "=") {
		for _, tok := range strings.Fields(s) {
			kv := strings.SplitN(tok, "=", 2)
			if len(kv) != 2 {
				continue
			}
			val := strings.TrimSpace(kv[1])
			if val == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(kv[0])) {
			case "name":
				out[fName] = val
			case "host":
				out[fHost] = val
			case "port":
				out[fPort] = val
			case "user":
				out[fUser] = val
			case "identity", "key", "id":
				out[fIdentity] = val
			case "remote", "remote_path", "src":
				out[fRemote] = val
			case "local", "local_path", "dst":
				out[fLocal] = val
			}
		}
		return out
	}
	// scp shorthand: [user@]host:/remote/path
	rest := s
	if i := strings.Index(rest, "@"); i >= 0 {
		if u := strings.TrimSpace(rest[:i]); u != "" {
			out[fUser] = u
		}
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 {
		if h := strings.TrimSpace(rest[:i]); h != "" {
			out[fHost] = h
		}
		if p := strings.TrimSpace(rest[i+1:]); p != "" {
			out[fRemote] = p
		}
	} else if h := strings.TrimSpace(rest); h != "" {
		out[fHost] = h
	}
	return out
}

// applyPaste parses the paste field and fills the matching inputs.
func (m *formModel) applyPaste() {
	fields := parsePaste(m.paste.Value())
	if len(fields) == 0 {
		m.status, m.statusErr = "粘贴解析: 未识别任何字段", true
		return
	}
	for idx, v := range fields {
		m.inputs[idx].SetValue(v)
	}
	m.paste.SetValue("")
	m.status, m.statusErr = fmt.Sprintf("粘贴解析: 已填充 %d 个字段", len(fields)), false
}

func setIntPtr(ti *textinput.Model, p *int) {
	if p != nil {
		ti.SetValue(strconv.Itoa(*p))
	}
}

func (m formModel) snapshot() map[string]string {
	s := map[string]string{
		"strict": strconv.FormatBool(m.strict),
		"ignore": m.ignore.Value(),
	}
	for i := range m.inputs {
		s[fmt.Sprintf("in%d", i)] = m.inputs[i].Value()
	}
	for i := range m.ret {
		s[fmt.Sprintf("ret%d", i)] = m.ret[i].Value()
	}
	return s
}

func (m formModel) isDirty() bool { return !reflect.DeepEqual(m.snapshot(), m.initial) }

func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func (m formModel) retentionOverride() (*config.RetentionOverride, error) {
	parse := func(label string, ti textinput.Model) (*int, error) {
		v := strings.TrimSpace(ti.Value())
		if v == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("保留覆盖 %s 必须是数字: %q", label, v)
		}
		return &n, nil
	}
	r, err := parse("recent", m.ret[0])
	if err != nil {
		return nil, err
	}
	mo, err := parse("monthly", m.ret[1])
	if err != nil {
		return nil, err
	}
	se, err := parse("semiannual", m.ret[2])
	if err != nil {
		return nil, err
	}
	ye, err := parse("yearly", m.ret[3])
	if err != nil {
		return nil, err
	}
	if r == nil && mo == nil && se == nil && ye == nil {
		return nil, nil
	}
	return &config.RetentionOverride{Recent: r, Monthly: mo, Semiannual: se, Yearly: ye}, nil
}

func (m formModel) toSync() (config.Sync, error) {
	s := config.Sync{
		Name:          strings.TrimSpace(m.inputs[fName].Value()),
		Host:          strings.TrimSpace(m.inputs[fHost].Value()),
		User:          strings.TrimSpace(m.inputs[fUser].Value()),
		Identity:      strings.TrimSpace(m.inputs[fIdentity].Value()),
		RemotePath:    strings.TrimSpace(m.inputs[fRemote].Value()),
		LocalPath:     strings.TrimSpace(m.inputs[fLocal].Value()),
		StrictHostKey: m.strict,
		Ignore:        splitLines(m.ignore.Value()),
	}
	if v := strings.TrimSpace(m.inputs[fPort].Value()); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return config.Sync{}, fmt.Errorf("端口必须是数字: %q", v)
		}
		s.Port = p
	}
	ov, err := m.retentionOverride()
	if err != nil {
		return config.Sync{}, err
	}
	s.Retention = ov
	return s, nil
}

// save validates a candidate config, persists it, and commits it in memory.
func (m *formModel) save() tea.Cmd {
	s, err := m.toSync()
	if err != nil {
		m.status, m.statusErr = err.Error(), true
		return nil
	}
	cand := *m.cfg
	cand.Sync = append([]config.Sync(nil), m.cfg.Sync...)
	if m.origIdx >= 0 {
		cand.Sync[m.origIdx] = s
	} else {
		cand.Sync = append(cand.Sync, s)
	}
	if err := cand.Validate(); err != nil {
		m.status, m.statusErr = err.Error(), true
		return nil
	}
	if err := config.Save(m.cfgPath, &cand); err != nil {
		m.status, m.statusErr = "保存失败: "+err.Error(), true
		return nil
	}
	*m.cfg = cand
	return tea.Batch(
		func() tea.Msg { return configChangedMsg{} },
		func() tea.Msg { return backToListMsg{} },
	)
}

func (m *formModel) applyFocus() {
	// setFocus toggles focus and colors the "> " prompt so the active control
	// stands out even when its label is off-screen.
	setFocus := func(ti *textinput.Model, on bool) {
		if on {
			ti.Focus()
			ti.PromptStyle = styleLabelOn
		} else {
			ti.Blur()
			ti.PromptStyle = styleHelp
		}
	}
	for i := range m.inputs {
		setFocus(&m.inputs[i], i == m.focus)
	}
	for i := range m.ret {
		setFocus(&m.ret[i], focusRet0+i == m.focus)
	}
	if m.focus == focusIgnore {
		m.ignore.Focus()
	} else {
		m.ignore.Blur()
	}
	setFocus(&m.paste, m.focus == focusPaste)
}

// applySize fits the text controls to the current terminal width/height. Labels
// occupy a fixed left column ("%-10s "), so inputs are sized to what remains;
// widths are clamped to a minimum so a tiny terminal cannot produce a
// zero/negative width.
func (m *formModel) applySize() {
	if m.width <= 0 {
		return
	}
	inW := clampMin(m.width-14, 10)
	for i := range m.inputs {
		m.inputs[i].Width = inW
	}
	m.paste.Width = inW
	m.ignore.SetWidth(clampMin(m.width-4, 20))
	if m.height > 0 {
		// Fixed rows around the ignore box: title block (2), 7 inputs, strict,
		// ignore header, retention, status (2), App footer (2) — plus the
		// 3-line quick-paste block on new-entry forms.
		reserve := 16
		if m.origIdx < 0 {
			reserve += 3
		}
		m.ignore.SetHeight(clampMin(m.height-reserve, 3))
	}
}

func (m formModel) Init() tea.Cmd { return textinput.Blink }

func (m formModel) Update(msg tea.Msg) (formModel, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = sz.Width, sz.Height
		m.applySize()
		return m, nil
	}
	key, isKey := msg.(tea.KeyMsg)
	if isKey && m.confirming {
		switch key.String() {
		case "y", "Y":
			if m.exitQuit {
				return m, func() tea.Msg { return quitMsg{} }
			}
			return m, func() tea.Msg { return backToListMsg{} }
		default:
			m.confirming = false
			return m, nil
		}
	}
	if isKey {
		switch key.String() {
		case "ctrl+s":
			return m, m.save()
		case "esc":
			if m.isDirty() {
				m.confirming, m.exitQuit = true, false
				return m, nil
			}
			return m, func() tea.Msg { return backToListMsg{} }
		case "ctrl+c":
			if m.isDirty() {
				m.confirming, m.exitQuit = true, true
				return m, nil
			}
			return m, func() tea.Msg { return quitMsg{} }
		case "tab":
			m.focus = (m.focus + 1) % m.focusCount()
			m.applyFocus()
			return m, nil
		case "shift+tab":
			n := m.focusCount()
			m.focus = (m.focus - 1 + n) % n
			m.applyFocus()
			return m, nil
		case "down":
			// inside the multi-line ignore box, ↓ moves the cursor down a
			// line; only when already on the last line does it jump to the
			// next field.
			if m.focus != focusIgnore || m.ignore.Line() >= m.ignore.LineCount()-1 {
				m.focus = (m.focus + 1) % m.focusCount()
				m.applyFocus()
				return m, nil
			}
		case "up":
			// symmetric to ↓: ↑ on the first line of the ignore box leaves it
			// for the previous field.
			if m.focus != focusIgnore || m.ignore.Line() <= 0 {
				n := m.focusCount()
				m.focus = (m.focus - 1 + n) % n
				m.applyFocus()
				return m, nil
			}
		case "enter":
			if m.focus == focusPaste {
				m.applyPaste()
				return m, nil
			}
		case " ":
			if m.focus == focusStrict {
				m.strict = !m.strict
				return m, nil
			}
		}
	}

	// route to the focused control
	var cmd tea.Cmd
	switch {
	case m.focus == focusPaste:
		m.paste, cmd = m.paste.Update(msg)
	case m.focus < numInputs:
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	case m.focus == focusIgnore:
		m.ignore, cmd = m.ignore.Update(msg)
	case m.focus >= focusRet0:
		i := m.focus - focusRet0
		m.ret[i], cmd = m.ret[i].Update(msg)
	}
	return m, cmd
}

// fieldLabel renders a fixed-width row label. Padding is computed from display
// width (CJK labels are wider in bytes than in columns, so %-10s alignment
// drifts) and the focused row gets a marker plus the accent color.
func fieldLabel(text string, focused bool) string {
	const w = 10
	if pad := w - lipgloss.Width(text); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	if focused {
		return styleLabelOn.Render("▸ "+text) + " "
	}
	return styleHelp.Render("  "+text) + " "
}

func (m formModel) View() string {
	var b strings.Builder
	title := "新增条目"
	if m.origIdx >= 0 {
		title = "编辑条目 · " + m.cfg.Sync[m.origIdx].Name
	}
	b.WriteString(styleTitleChip.Render("gsyncer") + " " + styleTitle.Render(title) + "\n")
	b.WriteString(rule(m.width) + "\n")
	if m.origIdx < 0 {
		b.WriteString(fieldLabel("快速粘贴", m.focus == focusPaste) + m.paste.View() + "\n")
		b.WriteString(styleHelp.Render("  ↑ 在此粘贴连接串后按 enter 自动解析填充") + "\n\n")
	}
	labels := []string{"名称", "主机", "端口", "用户", "密钥", "远程路径", "本地路径"}
	for i := range m.inputs {
		b.WriteString(fieldLabel(labels[i], m.focus == i) + m.inputs[i].View() + "\n")
	}
	strictMark := styleHelp.Render("[ ]")
	if m.strict {
		strictMark = styleLabelOn.Render("[x]")
	}
	b.WriteString(fieldLabel("strict", m.focus == focusStrict) + strictMark +
		" 严格检查 host key " + styleHelp.Render("(空格切换)") + "\n")
	b.WriteString(fieldLabel("忽略规则", m.focus == focusIgnore) +
		styleHelp.Render("gitignore 风格, 每行一条") + "\n" + m.ignore.View() + "\n")
	retFocus := m.focus >= focusRet0 && m.focus < focusRet0+4
	b.WriteString(fieldLabel("保留覆盖", retFocus) +
		fmt.Sprintf("recent[%s] monthly[%s] semi[%s] yearly[%s] %s\n",
			m.ret[0].View(), m.ret[1].View(), m.ret[2].View(), m.ret[3].View(),
			styleHelp.Render("留空=默认")))
	if m.confirming {
		b.WriteString("\n" + styleConfirm.Render("放弃未保存的改动？(y/N)"))
	} else if m.status != "" {
		if m.statusErr {
			b.WriteString("\n" + styleErr.Render("✘ "+m.status))
		} else {
			b.WriteString("\n" + styleStatus.Render("✔ "+m.status))
		}
	}
	return b.String()
}
