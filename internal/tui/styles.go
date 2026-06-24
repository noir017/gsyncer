package tui

import "github.com/charmbracelet/lipgloss"

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleStatus   = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleDotOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).SetString("●")
	styleDotFail  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).SetString("●")
	styleDotNever = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).SetString("○")
)
