package tui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/opolancoh/dbsync/internal/adapters"
	"github.com/opolancoh/dbsync/internal/compare"
	"github.com/opolancoh/dbsync/internal/extract"
	"github.com/opolancoh/dbsync/internal/config"
	"github.com/opolancoh/dbsync/internal/inspect"
	"github.com/opolancoh/dbsync/internal/transfer"
)

// ── Steps ────────────────────────────────────────────────────────────────────

type step int

const (
	stepConfig step = iota
	stepInspect
	stepBackup
	stepAnalyze
	stepTransfer
	stepDone
)

type stepInfo struct {
	label       string
	description string
}

var steps = []stepInfo{
	{label: "Config",   description: "Enter source and target connections"},
	{label: "Inspect",  description: "Read tables and columns from source DB"},
	{label: "Extract",  description: "Export source DB table rows to NDJSON files"},
	{label: "Compare",  description: "Compare source schema against target DB"},
	{label: "Transfer", description: "Load NDJSON files into target DB"},
}

// ── Messages ─────────────────────────────────────────────────────────────────

type errMsg struct{ err error }

type inspectDoneMsg struct {
	adapter   adapters.DBAdapter
	schema    *inspect.Schema
	backupDir string
}

type backupProgressMsg struct {
	table   string
	rows    int
	skipped bool
}

type backupDoneMsg struct{ manifest *extract.Summary }

type analyzeDoneMsg struct {
	mapping *compare.Mapping
}

type transferProgressMsg struct {
	table   string
	rows    int
	skipped bool
	err     error
}

type transferDoneMsg struct {
	summary *transfer.Summary
	logPath string
}

type connTestDoneMsg struct{ err error }

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	activeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ── Model ────────────────────────────────────────────────────────────────────

type Model struct {
	step    step
	running bool
	err     error

	// config form
	inputs []textinput.Model
	focus  int

	// context (cancelled on quit to stop in-flight DB ops)
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *config.Config

	// source adapter: created in inspect, closed after extract
	srcAdapter adapters.DBAdapter

	// step results
	backupDir string
	schema    *inspect.Schema
	manifest  *extract.Summary
	mapping   *compare.Mapping
	summary   *transfer.Summary
	logPath   string

	// reorder cursor: index into mapping.Tables on the compare screen
	cursor int

	// streaming progress
	progressCh       <-chan tea.Msg
	logs             []string                       // extract step lines
	transferProgress map[string]transferProgressMsg // transfer step per-table results

	spinner spinner.Model
	width   int
	height  int
}

func New() Model {
	inputs := make([]textinput.Model, 3)

	inputs[0] = textinput.New()
	inputs[0].Placeholder = "./config.yaml"
	inputs[0].SetValue("./config.yaml")
	inputs[0].Focus()
	inputs[0].Width = 60

	inputs[1] = textinput.New()
	inputs[1].Placeholder = "postgres://user:password@host:5432/dbname"
	inputs[1].Width = 60

	inputs[2] = textinput.New()
	inputs[2].Placeholder = "postgres://user:password@host:5432/dbname (for analyze & transfer)"
	inputs[2].Width = 60

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		inputs:  inputs,
		spinner: s,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputWidth := msg.Width - 4
		if inputWidth < 20 {
			inputWidth = 20
		}
		for i := range m.inputs {
			m.inputs[i].Width = inputWidth
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.closeAdapters()
			m.cancel()
			return m, tea.Quit
		case "q":
			if !m.running {
				m.closeAdapters()
				m.cancel()
				return m, tea.Quit
			}
		case "r":
			if m.step == stepDone {
				m.step = stepAnalyze
				m.running = false
				m.logs = nil
				m.summary = nil
				m.logPath = ""
				m.transferProgress = nil
				return m, nil
			}
		case "enter":
			if m.step == stepConfig && !m.running {
				return m.updateConfig(msg)
			}
			if !m.running && m.err == nil {
				return m.advance()
			}
		case "up":
			if m.step == stepConfig && !m.running {
				return m.updateConfig(msg)
			}
			if m.step == stepAnalyze && m.mapping != nil && !m.running && m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.step == stepConfig && !m.running {
				return m.updateConfig(msg)
			}
			if m.step == stepAnalyze && m.mapping != nil && !m.running && m.cursor < len(m.mapping.Tables)-1 {
				m.cursor++
			}
		case "+":
			if m.step == stepAnalyze && m.mapping != nil && !m.running && m.cursor > 0 {
				i := m.cursor
				m.mapping.Tables[i], m.mapping.Tables[i-1] = m.mapping.Tables[i-1], m.mapping.Tables[i]
				m.mapping.Tables[i].Order, m.mapping.Tables[i-1].Order = m.mapping.Tables[i-1].Order, m.mapping.Tables[i].Order
				m.cursor--
			}
		case "-":
			if m.step == stepAnalyze && m.mapping != nil && !m.running && m.cursor < len(m.mapping.Tables)-1 {
				i := m.cursor
				m.mapping.Tables[i], m.mapping.Tables[i+1] = m.mapping.Tables[i+1], m.mapping.Tables[i]
				m.mapping.Tables[i].Order, m.mapping.Tables[i+1].Order = m.mapping.Tables[i+1].Order, m.mapping.Tables[i].Order
				m.cursor++
			}
		case "tab", "shift+tab":
			if m.step == stepConfig && !m.running {
				return m.updateConfig(msg)
			}
		default:
			if m.step == stepConfig && !m.running {
				return m.updateConfig(msg)
			}
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case errMsg:
		m.err = msg.err
		m.running = false
		return m, nil

	case inspectDoneMsg:
		m.srcAdapter = msg.adapter
		m.schema = msg.schema
		m.backupDir = msg.backupDir
		m.running = false
		return m, nil

	case backupProgressMsg:
		if msg.skipped {
			m.logs = append(m.logs, dimStyle.Render(fmt.Sprintf("  - %-38s no rows, skipped", msg.table)))
		} else {
			m.logs = append(m.logs, successStyle.Render(fmt.Sprintf("  ✓ %-38s %d rows", msg.table, msg.rows)))
		}
		return m, listenForProgress(m.progressCh)

	case backupDoneMsg:
		m.manifest = msg.manifest
		m.running = false
		return m, nil

	case connTestDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.running = false
			return m, nil
		}
		m.step = stepInspect
		return m, tea.Batch(m.spinner.Tick, m.cmdInspect())

	case analyzeDoneMsg:
		m.mapping = msg.mapping
		m.cursor = 0
		m.running = false
		return m, nil

	case transferProgressMsg:
		m.transferProgress[msg.table] = msg
		return m, listenForProgress(m.progressCh)

	case transferDoneMsg:
		m.summary = msg.summary
		m.logPath = msg.logPath
		m.running = false
		m.step = stepDone
		m.closeAdapters()
		return m, nil
	}

	return m, nil
}

func (m Model) updateConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "down":
		m.focus = (m.focus + 1) % len(m.inputs)
	case "shift+tab", "up":
		m.focus = (m.focus - 1 + len(m.inputs)) % len(m.inputs)
	case "enter":
		if m.focus < len(m.inputs)-1 {
			m.focus++
		} else {
			return m.submitConfig()
		}
	default:
		m.err = nil
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
		return m, cmd
	}

	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		if i == m.focus {
			cmds[i] = m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) submitConfig() (tea.Model, tea.Cmd) {
	configPath := strings.TrimSpace(m.inputs[0].Value())
	sourceConn := strings.TrimSpace(m.inputs[1].Value())
	targetConn := strings.TrimSpace(m.inputs[2].Value())

	if configPath == "" {
		configPath = "./config.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		m.err = fmt.Errorf("loading config: %w", err)
		return m, nil
	}
	cfg.SourceConn = sourceConn
	cfg.TargetConn = targetConn

	if sourceConn != "" && targetConn != "" && sourceConn == targetConn {
		m.err = fmt.Errorf("source and target connections must be different")
		return m, nil
	}

	if err := cfg.Validate(true, true); err != nil {
		m.err = err
		return m, nil
	}

	m.cfg = cfg
	m.running = true
	m.err = nil

	return m, tea.Batch(m.spinner.Tick, m.cmdTestConnections())
}

func (m Model) advance() (tea.Model, tea.Cmd) {
	switch m.step {

	case stepInspect:
		m.step = stepBackup
		m.running = true
		m.logs = nil
		ch := make(chan tea.Msg, 50)
		m.progressCh = ch
		ctx, adapter, schema := m.ctx, m.srcAdapter, m.schema
		m.srcAdapter = nil
		extractDir := filepath.Join(m.backupDir, "02-extract")
		go func() {
			defer adapter.Close(ctx)
			manifest, err := extract.Run(ctx, adapter, schema, extractDir, func(table string, rows int, skipped bool) {
				ch <- backupProgressMsg{table: table, rows: rows, skipped: skipped}
			})
			if err != nil {
				ch <- errMsg{fmt.Errorf("backup: %w", err)}
			} else {
				ch <- backupDoneMsg{manifest: manifest}
			}
			close(ch)
		}()
		return m, listenForProgress(m.progressCh)

	case stepBackup:
		m.step = stepAnalyze
		m.running = true
		m.logs = nil
		return m, tea.Batch(m.spinner.Tick, m.cmdAnalyze())

	case stepAnalyze:
		if err := compare.Save(m.mapping, filepath.Join(m.backupDir, "03-compare")); err != nil {
			m.err = fmt.Errorf("saving mapping: %w", err)
			return m, nil
		}
		m.step = stepTransfer
		m.running = true
		m.logs = nil
		m.transferProgress = make(map[string]transferProgressMsg)
		ch := make(chan tea.Msg, 50)
		m.progressCh = ch
		ctx, cfg, mapping := m.ctx, m.cfg, m.mapping
		backupDir := m.backupDir
		extractDir := filepath.Join(backupDir, "02-extract")
		go func() {
			tgtAdapter, err := adapters.NewPostgresAdapter(ctx, cfg.TargetConn)
			if err != nil {
				ch <- errMsg{fmt.Errorf("connecting to target: %w", err)}
				close(ch)
				return
			}
			defer tgtAdapter.Close(ctx)

			opts := transfer.Options{
				ChunkSize:   500,
				NoInterrupt: true,
				OnProgress: func(table string, rows int, skipped bool, err error) {
					ch <- transferProgressMsg{table: table, rows: rows, skipped: skipped, err: err}
				},
			}
			summary, err := transfer.Run(ctx, tgtAdapter, mapping, extractDir, opts)
			if err != nil {
				ch <- errMsg{fmt.Errorf("transfer: %w", err)}
				close(ch)
				return
			}
			logPath := writeTransferLog(summary, filepath.Join(backupDir, "04-transfer"))
			ch <- transferDoneMsg{summary: summary, logPath: logPath}
			close(ch)
		}()
		return m, listenForProgress(m.progressCh)

	case stepDone:
		m.closeAdapters()
		m.cancel()
		return m, tea.Quit
	}

	return m, nil
}

func (m *Model) closeAdapters() {
	if m.srcAdapter != nil {
		m.srcAdapter.Close(m.ctx)
		m.srcAdapter = nil
	}
}

// ── Commands ─────────────────────────────────────────────────────────────────

func (m Model) cmdInspect() tea.Cmd {
	ctx, cfg := m.ctx, m.cfg
	return func() tea.Msg {
		adapter, err := adapters.NewPostgresAdapter(ctx, cfg.SourceConn)
		if err != nil {
			return errMsg{fmt.Errorf("connecting to source: %w", err)}
		}

		dbName, _ := dbNameFromConn(cfg.SourceConn)
		host, _ := hostFromConn(cfg.SourceConn)
		rootDir := filepath.Join(cfg.Output.Directory, time.Now().UTC().Format("2006-01-02T15-04-05Z")+"_"+dbName)

		schema, err := inspect.Run(ctx, adapter, cfg.Source.Engine, host, dbName, filepath.Join(rootDir, "01-inspect"), cfg.IgnoredTables)
		if err != nil {
			adapter.Close(ctx)
			return errMsg{fmt.Errorf("inspect: %w", err)}
		}

		return inspectDoneMsg{adapter: adapter, schema: schema, backupDir: rootDir}
	}
}

func (m Model) cmdAnalyze() tea.Cmd {
	ctx, cfg, schema, backupDir, manifest := m.ctx, m.cfg, m.schema, m.backupDir, m.manifest
	return func() tea.Msg {
		adapter, err := adapters.NewPostgresAdapter(ctx, cfg.TargetConn)
		if err != nil {
			return errMsg{fmt.Errorf("connecting to target: %w", err)}
		}

		var skippedTables []string
		if manifest != nil {
			skippedTables = manifest.SkippedTables
		}

		mapping, err := compare.Run(ctx, adapter, schema, filepath.Join(backupDir, "03-compare"), skippedTables)
		adapter.Close(ctx)
		if err != nil {
			return errMsg{fmt.Errorf("analyze: %w", err)}
		}

		return analyzeDoneMsg{mapping: mapping}
	}
}

func (m Model) cmdTestConnections() tea.Cmd {
	ctx, cfg := m.ctx, m.cfg
	return func() tea.Msg {
		src, err := adapters.NewPostgresAdapter(ctx, cfg.SourceConn)
		if err != nil {
			return connTestDoneMsg{fmt.Errorf("source connection failed: %w", err)}
		}
		src.Close(ctx)

		tgt, err := adapters.NewPostgresAdapter(ctx, cfg.TargetConn)
		if err != nil {
			return connTestDoneMsg{fmt.Errorf("target connection failed: %w", err)}
		}
		tgt.Close(ctx)

		return connTestDoneMsg{}
	}
}

func listenForProgress(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + titleStyle.Render("dbsync") + "\n\n")
	b.WriteString(m.stepList() + "\n")

	if m.err != nil && m.step != stepConfig {
		b.WriteString(errStyle.Render("  ✗ "+m.err.Error()) + "\n\n")
		b.WriteString(helpStyle.Render("  [q] quit") + "\n")
		return b.String()
	}

	switch m.step {
	case stepConfig:
		b.WriteString(m.viewConfig())
	case stepInspect:
		b.WriteString(m.viewInspect())
	case stepBackup:
		b.WriteString(m.viewExtract())
	case stepAnalyze:
		b.WriteString(m.viewCompare())
	case stepTransfer:
		b.WriteString(m.viewTransfer())
	case stepDone:
		b.WriteString(m.viewDone())
	}

	return b.String()
}

func (m Model) stepList() string {
	var b strings.Builder
	for i, info := range steps {
		s := step(i) // stepConfig=0, stepInspect=1, stepExtract=2, stepCompare=3, stepTransfer=4
		var icon, name, desc string
		switch {
		case m.step == stepDone || m.step > s:
			icon = successStyle.Render("✓")
			name = dimStyle.Render(info.label)
			desc = dimStyle.Render(info.description)
		case m.step == s && m.running:
			icon = activeStyle.Render("⠋")
			name = activeStyle.Render(info.label)
			desc = info.description
		case m.step == s:
			icon = activeStyle.Render("●")
			name = activeStyle.Render(info.label)
			desc = info.description
		default:
			icon = dimStyle.Render("○")
			name = dimStyle.Render(info.label)
			desc = dimStyle.Render(info.description)
		}
		b.WriteString(fmt.Sprintf("  %s  %-10s %s\n", icon, name, desc))
	}
	return b.String()
}

func (m Model) viewConfig() string {
	var b strings.Builder
	labels := []string{"Config file path", "Source connection", "Target connection"}

	for i, input := range m.inputs {
		b.WriteString("  " + labelStyle.Render(labels[i]) + "\n")
		b.WriteString("  " + input.View() + "\n\n")
	}

	if m.cfg != nil {
		b.WriteString(dimStyle.Render("  From config.yaml") + "\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("    %-16s %s", "Engine:", m.cfg.Source.Engine)) + "\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("    %-16s %s", "Output dir:", m.cfg.Output.Directory)) + "\n")
		if len(m.cfg.IgnoredTables) > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("    %-16s %s", "Ignored tables:", strings.Join(m.cfg.IgnoredTables, ", "))) + "\n")
		}
		b.WriteString("\n")
	}

	switch {
	case m.running:
		b.WriteString("  " + m.spinner.View() + " Testing connections...\n")
	case m.err != nil:
		firstLine := strings.SplitN(m.err.Error(), "\n", 2)[0]
		b.WriteString(errStyle.Render("  ✗ "+firstLine) + "\n")
		if strings.Contains(strings.ToLower(m.err.Error()), "tls") {
			b.WriteString(helpStyle.Render("    Hint: append ?sslmode=disable to the connection string") + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  [tab] next field") + "\n")
		b.WriteString(helpStyle.Render("  [enter] retry") + "\n")
		b.WriteString(helpStyle.Render("  [ctrl+c] quit") + "\n")
	default:
		b.WriteString(helpStyle.Render("  [tab] next field") + "\n")
		b.WriteString(helpStyle.Render("  [enter] confirm field / start") + "\n")
		b.WriteString(helpStyle.Render("  [ctrl+c] quit") + "\n")
	}

	return b.String()
}

func (m Model) viewInspect() string {
	var b strings.Builder

	if m.schema != nil {
		b.WriteString(fmt.Sprintf("  %-16s %s @ %s\n", "Source:", m.schema.Database, m.schema.Host))
		b.WriteString(fmt.Sprintf("  %-16s %d tables found\n", "Tables:", len(m.schema.Tables)))
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s %s", "Folder:", filepath.Base(m.backupDir))) + "\n")
		b.WriteString("\n")
	}

	if m.running {
		b.WriteString("  " + m.spinner.View() + " Connecting and reading schema...\n")
		return b.String()
	}

	if m.schema != nil {
		for _, t := range m.schema.Tables {
			b.WriteString(dimStyle.Render(fmt.Sprintf("    %-35s %d fields", t.Name, len(t.Fields))) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  [enter] start extract") + "\n")
		b.WriteString(helpStyle.Render("  [q] quit") + "\n")
	}

	return b.String()
}

func (m Model) viewExtract() string {
	var b strings.Builder

	if m.schema != nil {
		b.WriteString(fmt.Sprintf("  %-16s %s @ %s\n", "Source DB:", m.schema.Database, m.schema.Host))
	}
	if m.manifest != nil {
		totalRows := 0
		for _, t := range m.manifest.Tables {
			totalRows += t.Rows
		}
		b.WriteString(fmt.Sprintf("  %-16s %d\n", "Tables exported:", len(m.manifest.Tables)))
		b.WriteString(fmt.Sprintf("  %-16s %d\n", "Rows total:", totalRows))
		if len(m.manifest.SkippedTables) > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s %d (no rows)", "Skipped:", len(m.manifest.SkippedTables))) + "\n")
		}
		b.WriteString("\n")
	}

	for _, line := range m.logs {
		b.WriteString(line + "\n")
	}

	if m.running {
		b.WriteString("  " + m.spinner.View() + "\n")
		return b.String()
	}

	if m.manifest != nil {
		b.WriteString(helpStyle.Render("  [enter] start compare") + "\n")
		b.WriteString(helpStyle.Render("  [q] quit") + "\n")
	}

	return b.String()
}

func (m Model) viewCompare() string {
	var b strings.Builder

	if m.cfg != nil {
		tgtDB, _ := dbNameFromConn(m.cfg.TargetConn)
		tgtHost, _ := hostFromConn(m.cfg.TargetConn)
		b.WriteString(fmt.Sprintf("  %-16s %s @ %s\n", "Target DB:", tgtDB, tgtHost))
	}
	if m.mapping != nil {
		matched, unmatched := 0, 0
		for _, t := range m.mapping.Tables {
			if t.Status == compare.StatusMatched {
				matched++
			} else {
				unmatched++
			}
		}
		b.WriteString(fmt.Sprintf("  %-16s %d\n", "Matched:", matched))
		if unmatched > 0 {
			b.WriteString(warnStyle.Render(fmt.Sprintf("  %-16s %d — will be skipped", "Unmatched:", unmatched)) + "\n")
		} else {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s %d", "Unmatched:", unmatched)) + "\n")
		}
		b.WriteString("\n")
	}

	if m.running {
		b.WriteString("  " + m.spinner.View() + " Comparing schemas...\n")
		return b.String()
	}

	if m.mapping != nil {
		for i, t := range m.mapping.Tables {
			targetName := "(unmatched)"
			if t.Target != nil {
				targetName = *t.Target
			}
			matchIcon := successStyle.Render("✓")
			if t.Status == compare.StatusUnmatched {
				matchIcon = warnStyle.Render("✗")
			}
			cursor := " "
			if i == m.cursor {
				cursor = activeStyle.Render("▶")
			}
			b.WriteString(fmt.Sprintf("  %s %s [%03d] %-30s → %s\n", cursor, matchIcon, t.Order, t.Source, targetName))
			for _, f := range t.Fields {
				if f.Status == compare.StatusUnmatched {
					detail := "not found in target"
					if f.Target != nil {
						detail = fmt.Sprintf("type mismatch: %s → %s", f.SourceType, *f.TargetType)
					}
					b.WriteString(dimStyle.Render(fmt.Sprintf("          ✗ %-28s %s", f.Source, detail)) + "\n")
				}
			}
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("  Reorder tables to satisfy foreign key dependencies before transferring.") + "\n\n")
		b.WriteString(helpStyle.Render("  [↑↓] select") + "\n")
		b.WriteString(helpStyle.Render("  [+/-] reorder") + "\n")
		b.WriteString(helpStyle.Render("  [enter] start transfer") + "\n")
		b.WriteString(helpStyle.Render("  [q] quit") + "\n")
	}

	return b.String()
}

func (m Model) viewTransfer() string {
	var b strings.Builder

	if m.mapping != nil {
		toTransfer, toSkip := transfer.SortedPlan(m.mapping)
		for _, t := range toTransfer {
			prog, done := m.transferProgress[t.Source]
			var icon, detail string
			switch {
			case done && prog.err != nil:
				icon = errStyle.Render("✗")
				detail = errStyle.Render(prog.err.Error())
			case done:
				icon = successStyle.Render("✓")
				detail = successStyle.Render(fmt.Sprintf("%d rows", prog.rows))
			default:
				icon = dimStyle.Render("·")
				detail = dimStyle.Render("pending")
			}
			b.WriteString(fmt.Sprintf("  %s %-38s %s\n", icon, t.Source, detail))
		}
		for _, t := range toSkip {
			reason := "unmatched"
			if t.Skip {
				reason = "skipped"
			}
			b.WriteString(fmt.Sprintf("  %s %-38s %s\n", dimStyle.Render("-"), t.Source, dimStyle.Render(reason)))
		}
		b.WriteString("\n")
	}

	if m.running {
		b.WriteString("  " + m.spinner.View() + " Transferring...\n")
	}

	return b.String()
}

func (m Model) viewDone() string {
	var b strings.Builder

	if m.mapping != nil {
		toTransfer, toSkip := transfer.SortedPlan(m.mapping)
		for _, t := range toTransfer {
			prog := m.transferProgress[t.Source]
			var icon, detail string
			if prog.err != nil {
				icon = errStyle.Render("✗")
				detail = errStyle.Render(prog.err.Error())
			} else {
				icon = successStyle.Render("✓")
				detail = successStyle.Render(fmt.Sprintf("%d rows", prog.rows))
			}
			b.WriteString(fmt.Sprintf("  %s %-38s %s\n", icon, t.Source, detail))
		}
		for _, t := range toSkip {
			reason := "unmatched"
			if t.Skip {
				reason = "skipped"
			}
			b.WriteString(fmt.Sprintf("  %s %-38s %s\n", dimStyle.Render("-"), t.Source, dimStyle.Render(reason)))
		}
		b.WriteString("\n")
	}

	transferred, skipped, failed, totalRows := 0, 0, 0, 0
	if m.summary != nil {
		for _, t := range m.summary.Tables {
			switch {
			case t.Skipped:
				skipped++
			case t.Err != nil:
				failed++
			default:
				transferred++
				totalRows += t.Rows
			}
		}
	}

	if m.schema != nil {
		b.WriteString(fmt.Sprintf("  %-16s %s @ %s\n", "Source DB:", m.schema.Database, m.schema.Host))
	}
	if m.cfg != nil {
		tgtDB, _ := dbNameFromConn(m.cfg.TargetConn)
		tgtHost, _ := hostFromConn(m.cfg.TargetConn)
		b.WriteString(fmt.Sprintf("  %-16s %s @ %s\n", "Target DB:", tgtDB, tgtHost))
	}
	b.WriteString(fmt.Sprintf("  %-20s %d\n", "Tables transferred:", transferred))
	b.WriteString(fmt.Sprintf("  %-20s %d\n", "Rows transferred:", totalRows))
	if skipped > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %-20s %d", "Tables skipped:", skipped)) + "\n")
	}
	if failed > 0 {
		b.WriteString(errStyle.Render(fmt.Sprintf("  %-20s %d", "Tables failed:", failed)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(successStyle.Render("  ✓ Done") + "\n\n")
	if m.logPath != "" {
		b.WriteString(dimStyle.Render("  Log: "+m.logPath) + "\n")
	}
	b.WriteString(helpStyle.Render("  [r] retry transfer") + "\n")
	b.WriteString(helpStyle.Render("  [enter] / [q] exit") + "\n")

	return b.String()
}

// ── Transfer log ─────────────────────────────────────────────────────────────

func writeTransferLog(summary *transfer.Summary, dir string) string {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	logPath := filepath.Join(dir, "transfer.log")
	f, err := os.Create(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	fmt.Fprintf(f, "Transfer Log\n")
	fmt.Fprintf(f, "============\n")
	fmt.Fprintf(f, "Transferred at: %s\n\n", summary.TransferredAt)

	var ok, skipped, failed []transfer.TableResult
	for _, t := range summary.Tables {
		switch {
		case t.Skipped:
			skipped = append(skipped, t)
		case t.Err != nil:
			failed = append(failed, t)
		default:
			ok = append(ok, t)
		}
	}

	totalRows := 0
	for _, t := range ok {
		totalRows += t.Rows
	}

	fmt.Fprintf(f, "TRANSFERRED (%d tables, %d rows):\n", len(ok), totalRows)
	for _, t := range ok {
		fmt.Fprintf(f, "  ✓ %-40s %d rows\n", t.Name, t.Rows)
	}

	if len(skipped) > 0 {
		fmt.Fprintf(f, "\nSKIPPED (%d tables):\n", len(skipped))
		for _, t := range skipped {
			fmt.Fprintf(f, "  - %-40s %s\n", t.Name, t.Reason)
		}
	}

	if len(failed) > 0 {
		fmt.Fprintf(f, "\nFAILED (%d tables):\n", len(failed))
		for _, t := range failed {
			fmt.Fprintf(f, "  ✗ %-40s %s\n", t.Name, t.Err)
		}
	}

	return logPath
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func dbNameFromConn(connString string) (string, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(u.Path, "/"), nil
}

func hostFromConn(connString string) (string, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}
