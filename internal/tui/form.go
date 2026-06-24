package tui

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
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
)

type formModel struct {
	cfg     *config.Config
	cfgPath string
	origIdx int // -1 == new

	inputs []textinput.Model // numInputs
	ignore textarea.Model
	ret    []textinput.Model // 4: recent, monthly, semiannual, yearly
	strict bool
	focus  int

	initial map[string]string // snapshot for dirty detection
	status  string

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
	m.ignore.SetHeight(4)
	m.ret = make([]textinput.Model, 4)
	for i := range m.ret {
		ti := textinput.New()
		ti.CharLimit = 6
		m.ret[i] = ti
	}

	if origIdx >= 0 && origIdx < len(cfg.Sync) {
		s := cfg.Sync[origIdx]
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
	m.applyFocus()
	m.initial = m.snapshot()
	return m
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
		m.status = err.Error()
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
		m.status = err.Error()
		return nil
	}
	if err := config.Save(m.cfgPath, &cand); err != nil {
		m.status = "保存失败: " + err.Error()
		return nil
	}
	*m.cfg = cand
	return tea.Batch(
		func() tea.Msg { return configChangedMsg{} },
		func() tea.Msg { return backToListMsg{} },
	)
}

func (m *formModel) applyFocus() {
	for i := range m.inputs {
		if i == m.focus {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
	for i := range m.ret {
		if focusRet0+i == m.focus {
			m.ret[i].Focus()
		} else {
			m.ret[i].Blur()
		}
	}
	if m.focus == focusIgnore {
		m.ignore.Focus()
	} else {
		m.ignore.Blur()
	}
}

func (m formModel) Init() tea.Cmd { return textinput.Blink }

func (m formModel) Update(msg tea.Msg) (formModel, tea.Cmd) {
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
		case "tab", "down":
			m.focus = (m.focus + 1) % numFocus
			m.applyFocus()
			return m, nil
		case "shift+tab", "up":
			m.focus = (m.focus - 1 + numFocus) % numFocus
			m.applyFocus()
			return m, nil
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

func (m formModel) View() string {
	var b strings.Builder
	title := "新增条目"
	if m.origIdx >= 0 {
		title = "编辑条目: " + m.cfg.Sync[m.origIdx].Name
	}
	b.WriteString(styleTitle.Render(title) + "\n\n")
	labels := []string{"名称", "主机", "端口", "用户", "密钥", "远程路径", "本地路径"}
	for i := range m.inputs {
		b.WriteString(fmt.Sprintf("%-10s %s\n", labels[i], m.inputs[i].View()))
	}
	strictMark := "[ ]"
	if m.strict {
		strictMark = "[x]"
	}
	b.WriteString(fmt.Sprintf("%-10s %s 严格检查 host key\n", "strict", strictMark))
	b.WriteString("忽略规则 (gitignore 风格, 每行一条):\n" + m.ignore.View() + "\n")
	b.WriteString(fmt.Sprintf("保留覆盖 recent[%s] monthly[%s] semi[%s] yearly[%s]\n",
		m.ret[0].View(), m.ret[1].View(), m.ret[2].View(), m.ret[3].View()))
	if m.confirming {
		b.WriteString("\n" + styleErr.Render("放弃未保存的改动？(y/N)"))
	} else if m.status != "" {
		b.WriteString("\n" + styleErr.Render(m.status))
	}
	b.WriteString("\n" + styleHelp.Render("tab/↓ 下一项  shift+tab/↑ 上一项  空格 切换 strict  ctrl+s 保存  esc 取消"))
	return b.String()
}
