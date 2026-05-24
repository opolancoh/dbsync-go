package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/opolancoh/dbsync/internal/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Interactive terminal UI for the full migration workflow",
	RunE:  runTUI,
}

func runTUI(_ *cobra.Command, _ []string) error {
	m := tui.New()
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
