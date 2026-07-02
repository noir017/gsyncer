package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette. Adaptive colors keep the UI readable on both light and dark
// terminals; every screen draws from this one set so the look stays coherent.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "26", Dark: "39"}   // primary blue
	colGood   = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}   // success green
	colBad    = lipgloss.AdaptiveColor{Light: "160", Dark: "203"} // error red
	colWarn   = lipgloss.AdaptiveColor{Light: "130", Dark: "214"} // warning orange
	colDim    = lipgloss.AdaptiveColor{Light: "246", Dark: "241"} // secondary text
	colSelBg  = lipgloss.AdaptiveColor{Light: "254", Dark: "237"} // selected-row background
	colOnDark = lipgloss.Color("231")                             // text on colored chips
)

var (
	// styleTitleChip is the inverted " gsyncer " badge that anchors every screen.
	styleTitleChip = lipgloss.NewStyle().Bold(true).Foreground(colOnDark).Background(colAccent).Padding(0, 1)
	styleTitle     = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	styleStatus  = lipgloss.NewStyle().Foreground(colGood)
	styleErr     = lipgloss.NewStyle().Foreground(colBad)
	styleWarn    = lipgloss.NewStyle().Foreground(colWarn)
	styleConfirm = lipgloss.NewStyle().Bold(true).Foreground(colOnDark).Background(colBad).Padding(0, 1)

	styleBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1)
	styleHelp    = lipgloss.NewStyle().Foreground(colDim)
	styleHelpKey = lipgloss.NewStyle().Foreground(colAccent)
	styleLabelOn = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	styleDotOK    = lipgloss.NewStyle().Foreground(colGood).SetString("●")
	styleDotFail  = lipgloss.NewStyle().Foreground(colBad).SetString("●")
	styleDotNever = lipgloss.NewStyle().Foreground(colDim).SetString("○")
)

// rule renders a dim horizontal separator w columns wide, falling back to a
// fixed width before the first WindowSizeMsg arrives.
func rule(w int) string {
	if w <= 0 {
		w = 40
	}
	return styleHelp.Render(strings.Repeat("─", w))
}

// helpKeys renders alternating key/description pairs for the footer, with the
// keys accented so they can be scanned at a glance.
func helpKeys(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(styleHelp.Render(" · "))
		}
		b.WriteString(styleHelpKey.Render(pairs[i]) + styleHelp.Render(" "+pairs[i+1]))
	}
	return b.String()
}
