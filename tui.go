package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type scanMsg struct {
	snapshot
	ID int
}
type deletedMsg struct {
	Path      string
	Size      int64
	Err       error
	Remaining *snapshot
}
type shutdownMsg struct{}

type model struct {
	deleteFn                                func(context.Context, entry, snapshot, string) error
	jobs                                    map[string]bool
	workers                                 sync.WaitGroup
	failureMu                               sync.Mutex
	failures                                []string
	quitting                                bool
	errorPath                               string
	root, home                              string
	ctx                                     context.Context
	cancel                                  context.CancelFunc
	data                                    snapshot
	scanning                                bool
	generation                              int
	width, height, cursor, offset, sortMode int
	rows                                    []entry
	children                                map[string][]entry
	expanded                                map[string]bool
	input                                   textinput.Model
	mode, filter, status                    string
	spin                                    spinner.Model
	dark                                    bool
	deleting                                bool
	pending                                 entry
	confirmYes                              bool
	modalOffset                             int
}

func newModel(root, home string) *model {
	ctx, cancel := context.WithCancel(context.Background())
	in := textinput.New()
	in.CharLimit = 4096
	s := spinner.New()
	s.Spinner = spinner.Dot
	return &model{root: root, home: home, ctx: ctx, cancel: cancel, width: 100, height: 30, input: in, spin: s, dark: true, expanded: map[string]bool{root: true}, jobs: map[string]bool{}, deleteFn: deleteTarget}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.beginScan(), m.spin.Tick, tea.RequestBackgroundColor)
}

func (m *model) beginScan() tea.Cmd {
	m.cancel()
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.generation++
	id, ctx, root := m.generation, m.ctx, m.root
	m.scanning = true
	m.status = "Scanning · Esc to cancel"
	return func() tea.Msg { return scanMsg{scan(ctx, root), id} }
}

func (m *model) index() {
	m.children = map[string][]entry{}
	for p, e := range m.data.Entries {
		if p != m.root {
			parent := filepath.Dir(p)
			m.children[parent] = append(m.children[parent], e)
		}
	}
	for p := range m.children {
		sort.Slice(m.children[p], func(i, j int) bool {
			a, b := m.children[p][i], m.children[p][j]
			if m.sortMode == 1 && !a.Info.ModTime().Equal(b.Info.ModTime()) {
				return a.Info.ModTime().After(b.Info.ModTime())
			}
			if m.sortMode != 2 && a.Size != b.Size {
				return a.Size > b.Size
			}
			return a.Path < b.Path
		})
	}
	m.rebuild()
}

func (m *model) rebuild() {
	m.rows = nil
	matches := map[string]bool{}
	if m.filter != "" {
		for p := range m.data.Entries {
			if strings.Contains(strings.ToLower(p), strings.ToLower(m.filter)) {
				for q := p; within(m.root, q); q = filepath.Dir(q) {
					matches[q] = true
					if q == m.root {
						break
					}
				}
			}
		}
	}
	var visit func(string)
	visit = func(p string) {
		for _, e := range m.children[p] {
			if m.filter != "" && !matches[e.Path] {
				continue
			}
			m.rows = append(m.rows, e)
			if e.Info.IsDir() && (m.expanded[e.Path] || m.filter != "") {
				visit(e.Path)
			}
		}
	}
	visit(m.root)
	m.cursor = min(m.cursor, max(0, len(m.rows)-1))
	m.ensureVisible()
}
func (m *model) pageSize() int { return max(1, m.height-12) }
func (m *model) ensureVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.pageSize() {
		m.offset = m.cursor - m.pageSize() + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.rows)-m.pageSize())))
}

func (m *model) prompt(mode, placeholder string) tea.Cmd {
	m.mode = mode
	m.input.SetValue("")
	m.input.Placeholder = placeholder
	return m.input.Focus()
}

func (m *model) toggle() {
	if len(m.rows) > 0 {
		e := m.rows[m.cursor]
		if e.Info.IsDir() {
			m.expanded[e.Path] = !m.expanded[e.Path]
			m.rebuild()
		}
	}
}

func (m *model) startDelete() tea.Cmd {
	e, home := m.pending, m.home
	// Workers own an immutable snapshot, never the live UI map.
	s := snapshot{Root: m.data.Root, Entries: map[string]entry{}, Cancelled: m.data.Cancelled}
	for p, item := range m.data.Entries {
		if within(e.Path, p) {
			s.Entries[p] = item
		}
	}
	m.mode = ""
	m.updateCache(deletedMsg{Path: e.Path}, false)
	m.jobs[e.Path] = true
	m.deleting = true
	m.status = "Deleting " + safe(e.Path)
	resultCh := make(chan deletedMsg, 1)
	deleteFn := m.deleteFn
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		err := deleteFn(context.Background(), e, s, home)
		result := deletedMsg{Path: e.Path, Size: e.Size, Err: err}
		if err != nil {
			fresh := scan(context.Background(), e.Path)
			result.Remaining = &fresh
			m.failureMu.Lock()
			m.failures = append(m.failures, e.Path+": "+err.Error())
			m.failureMu.Unlock()
		}
		resultCh <- result
	}()
	return func() tea.Msg { return <-resultCh }
}

func (m *model) requestQuit() tea.Cmd {
	m.quitting = true
	m.mode = ""
	m.cancel()
	if len(m.jobs) == 0 {
		return tea.Quit
	}
	m.status = "Waiting for background deletions to finish…"
	return nil
}

// Update the in-memory tree after cleanup. Only failed targets need a local
// refresh; deleting a file never triggers another scan of the root.
func (m *model) updateCache(v deletedMsg, finished bool) {
	oldSize := m.data.Entries[v.Path].Size
	for p := range m.data.Entries {
		if within(v.Path, p) {
			delete(m.data.Entries, p)
			delete(m.expanded, p)
		}
	}
	newSize := int64(0)
	if v.Remaining != nil {
		for p, e := range v.Remaining.Entries {
			m.data.Entries[p] = e
		}
		newSize = v.Remaining.Entries[v.Path].Size
	}
	for p := filepath.Dir(v.Path); within(m.root, p); p = filepath.Dir(p) {
		if e, ok := m.data.Entries[p]; ok {
			e.Size = max(0, e.Size-oldSize+newSize)
			if finished {
				if info, err := os.Lstat(p); err == nil {
					e.Info = info
				}
			}
			m.data.Entries[p] = e
		}
		if p == m.root {
			break
		}
	}
	m.index()
}

func (m *model) applyDeletion(v deletedMsg) {
	m.updateCache(v, true)
	delete(m.jobs, v.Path)
	m.deleting = len(m.jobs) > 0
	if v.Err != nil {
		m.status = "Could not finish deleting: " + v.Err.Error()
		m.errorPath = v.Path
		if m.mode == "" && !m.quitting {
			m.mode = "error"
		}
	} else {
		m.status = "Deleted " + safe(filepath.Base(v.Path)) + " · " + bytes(v.Size)
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case shutdownMsg:
		return m, m.requestQuit()
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.input.SetWidth(max(10, min(68, v.Width-10)))
		m.ensureVisible()
	case tea.BackgroundColorMsg:
		m.dark = v.IsDark()
	case scanMsg:
		if v.ID != m.generation {
			return m, nil
		}
		m.data = v.snapshot
		m.scanning = false
		m.status = fmt.Sprintf("%d entries · %s · r refresh", len(m.data.Entries), bytes(m.data.Entries[m.root].Size))
		if len(m.data.Errors) > 0 {
			m.status += fmt.Sprintf(" · %d unreadable (e)", len(m.data.Errors))
		}
		if m.data.Cancelled {
			m.status = "Scan cancelled · partial results; r to refresh"
		}
		m.index()
	case deletedMsg:
		m.applyDeletion(v)
		if m.quitting {
			return m, m.requestQuit()
		}
	case tea.MouseWheelMsg:
		if m.mode == "" && !m.quitting {
			if v.Button == tea.MouseWheelUp {
				m.cursor = max(0, m.cursor-3)
			} else {
				m.cursor = min(max(0, len(m.rows)-1), m.cursor+3)
			}
			m.ensureVisible()
		}
	case tea.MouseClickMsg:
		if m.mode == "" && !m.quitting && v.Button == tea.MouseLeft && v.Y >= 8 && v.Y < 8+m.pageSize() {
			i := m.offset + v.Y - 8
			if i < len(m.rows) {
				if m.cursor == i {
					m.toggle()
				} else {
					m.cursor = i
				}
			}
		}
	case tea.KeyPressMsg:
		key := v.String()
		if m.quitting {
			return m, nil
		}
		if key == "ctrl+c" {
			return m, m.requestQuit()
		}
		if m.mode == "confirm" {
			switch key {
			case "esc", "n", "q":
				m.mode = ""
			case "tab", "left", "right", "h", "l":
				m.confirmYes = !m.confirmYes
			case "down", "j":
				m.modalOffset++
			case "up", "k":
				m.modalOffset = max(0, m.modalOffset-1)
			case "y":
				return m, m.startDelete()
			case "enter":
				if m.confirmYes {
					return m, m.startDelete()
				}
				m.mode = ""
			}
			return m, nil
		}
		if m.mode == "help" || m.mode == "details" || m.mode == "errors" || m.mode == "error" {
			switch key {
			case "esc", "enter", "q":
				m.mode = ""
				m.modalOffset = 0
			case "down", "j":
				m.modalOffset++
			case "up", "k":
				m.modalOffset = max(0, m.modalOffset-1)
			}
			return m, nil
		}
		if m.mode != "" {
			if key == "esc" {
				m.mode = ""
				m.input.Blur()
				return m, nil
			}
			if key == "enter" {
				value := m.input.Value()
				if m.mode == "folder" {
					if value == "~" {
						value = m.home
					} else if strings.HasPrefix(value, "~/") {
						value = filepath.Join(m.home, value[2:])
					}
					path, err := filepath.Abs(value)
					if err == nil {
						path, err = filepath.EvalSymlinks(path)
					}
					if err != nil {
						m.status = err.Error()
						return m, nil
					}
					info, err := os.Stat(path)
					if err != nil || !info.IsDir() {
						m.status = "Enter an existing directory"
						return m, nil
					}
					m.mode = ""
					m.filter = ""
					if path == m.root {
						return m, nil
					}
					if _, ok := m.data.Entries[path]; ok && within(m.root, path) {
						for p := path; p != m.root; p = filepath.Dir(p) {
							m.expanded[p] = true
						}
						m.rebuild()
						for i, e := range m.rows {
							if e.Path == path {
								m.cursor = i
								break
							}
						}
						m.ensureVisible()
						return m, nil
					}
					m.root = path
					m.expanded = map[string]bool{path: true}
					m.data = snapshot{}
					m.rows = nil
					return m, m.beginScan()
				}
				m.filter = value
				m.mode = ""
				m.cursor = 0
				m.rebuild()
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch key {
		case "q":
			return m, m.requestQuit()
		case "esc":
			if m.scanning {
				m.cancel()
			} else {
				m.filter = ""
				m.rebuild()
			}
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
			m.ensureVisible()
		case "down", "j":
			m.cursor = min(max(0, len(m.rows)-1), m.cursor+1)
			m.ensureVisible()
		case "pgdown":
			m.cursor = min(max(0, len(m.rows)-1), m.cursor+m.pageSize())
			m.ensureVisible()
		case "pgup":
			m.cursor = max(0, m.cursor-m.pageSize())
			m.ensureVisible()
		case "enter", "space", " ":
			m.toggle()
		case "right", "l":
			if len(m.rows) > 0 {
				e := m.rows[m.cursor]
				if e.Info.IsDir() {
					if m.expanded[e.Path] {
						m.cursor = min(len(m.rows)-1, m.cursor+1)
					} else {
						m.expanded[e.Path] = true
						m.rebuild()
					}
					m.ensureVisible()
				}
			}
		case "left", "h", "backspace":
			if len(m.rows) > 0 {
				e := m.rows[m.cursor]
				if m.expanded[e.Path] {
					delete(m.expanded, e.Path)
					m.rebuild()
				} else {
					parent := filepath.Dir(e.Path)
					for i, row := range m.rows {
						if row.Path == parent {
							m.cursor = i
							break
						}
					}
					m.ensureVisible()
				}
			}
		case "/":
			return m, m.prompt("filter", "Filter the tree…")
		case "g":
			if len(m.jobs) > 0 {
				m.status = "Wait for pending deletions before changing scan folders"
				return m, nil
			}
			return m, m.prompt("folder", "Directory path…")
		case "s":
			m.sortMode = (m.sortMode + 1) % 3
			m.index()
		case "r":
			if len(m.jobs) > 0 {
				m.status = "Wait for pending deletions before refreshing"
				return m, nil
			}
			return m, m.beginScan()
		case "e":
			m.mode = "errors"
			m.modalOffset = 0
		case "i":
			if len(m.rows) > 0 {
				m.mode = "details"
				m.modalOffset = 0
			}
		case "?":
			m.mode = "help"
			m.modalOffset = 0
		case "d":
			if m.scanning || len(m.rows) == 0 {
				return m, nil
			}
			e := m.rows[m.cursor]
			for p := range m.jobs {
				if within(e.Path, p) || within(p, e.Path) {
					m.status = "This folder contains a pending deletion; wait for it to finish"
					return m, nil
				}
			}
			if protected(e.Path, m.root, m.home) || e.Incomplete || m.data.Cancelled {
				m.status = "Protected or incompletely scanned item; cannot delete"
				return m, nil
			}
			m.pending = e
			m.confirmYes = false
			m.modalOffset = 0
			m.mode = "confirm"
		}
	}
	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	return m, cmd
}

func bytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, s)
}
func clip(s string, w int) string { return ansi.Truncate(s, max(1, w), "…") }
func (m *model) shortPath(p string) string {
	if within(m.home, p) {
		return "~" + strings.TrimPrefix(p, m.home)
	}
	return p
}

func (m *model) View() tea.View {
	w := max(20, m.width)
	accent := lipgloss.Color("#A8B8EB")
	muted := lipgloss.Color("#81879C")
	fg := lipgloss.Color("#D9DDEF")
	bg := lipgloss.Color("#202331")
	selected := lipgloss.Color("#323A51")
	if !m.dark {
		accent = lipgloss.Color("#455C96")
		muted = lipgloss.Color("#667085")
		fg = lipgloss.Color("#202638")
		bg = lipgloss.Color("#FAFAFD")
		selected = lipgloss.Color("#E0E7F5")
	}
	a := lipgloss.NewStyle().Foreground(accent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(muted)
	total, free := capacity(m.root)
	system := "macOS / " + runtime.GOARCH
	if os.Geteuid() == 0 {
		system += " · administrator"
	}
	header := a.Render("›_ Sweep ") + dim.Render("(v"+version+")") + "\n\n" + dim.Render("system:    ") + system + "\n" + dim.Render("directory: ") + safe(m.shortPath(m.root)) + "\n" + dim.Render("disk:      ") + fmt.Sprintf("%s free / %s", bytes(int64(free)), bytes(int64(total)))
	// Five content rows plus borders: the tree always starts at row eight.
	headerLines := strings.Split(header, "\n")
	for i := range headerLines {
		headerLines[i] = clip(headerLines[i], min(72, w-6))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted).Padding(0, 1).Width(min(76, w-2)).Render(strings.Join(headerLines, "\n"))
	// Keep the header at seven lines even when its fixed width is smaller than the terminal.
	nameWidth := max(8, min(44, w-29))
	lines := []string{box, dim.Render(fmt.Sprintf(" %-*s %10s  %s", nameWidth, "FILES", []string{"SIZE ↓", "MODIFIED ↓", "NAME ↑"}[m.sortMode], "MODIFIED"))}
	for i := m.offset; i < min(len(m.rows), m.offset+m.pageSize()); i++ {
		e := m.rows[i]
		rel, _ := filepath.Rel(m.root, e.Path)
		depth := strings.Count(rel, string(os.PathSeparator))
		prefix := "  "
		if e.Info.IsDir() {
			prefix = "▸ "
			if m.expanded[e.Path] || m.filter != "" {
				prefix = "▾ "
			}
		}
		name := strings.Repeat("  ", min(depth, 12)) + prefix + safe(filepath.Base(e.Path))
		if e.Info.IsDir() {
			name += "/"
		}
		if e.Info.Mode()&os.ModeSymlink != 0 {
			name += " ↗"
		}
		if e.Incomplete {
			name += " [partial]"
		}
		name = clip(name, nameWidth)
		line := " " + lipgloss.NewStyle().Width(nameWidth).Render(name) + fmt.Sprintf(" %10s  %s", bytes(e.Size), e.Info.ModTime().Format("2006-01-02"))
		if i == m.cursor {
			line = lipgloss.NewStyle().Background(selected).Foreground(fg).Width(w).Render(line)
		}
		lines = append(lines, clip(line, w))
	}
	if len(m.rows) == 0 {
		empty := " Empty folder"
		if m.scanning {
			empty = " Scanning…"
		} else if m.filter != "" {
			empty = " No matches"
		}
		lines = append(lines, dim.Render(empty))
	}
	for len(lines) < m.pageSize()+2 {
		lines = append(lines, "")
	}
	status := m.status
	if m.scanning || m.deleting {
		status = m.spin.View() + " " + status
	}
	if m.deleting {
		status = fmt.Sprintf("%s · %d deletion(s) pending", status, len(m.jobs))
	}
	if m.filter != "" {
		status = "Filter: " + safe(m.filter) + " · " + status
	}
	lines = append(lines, dim.Render(clip(" "+status, w)), dim.Render(clip(" ↑↓ move  Enter expand  ← collapse  d delete  / filter  s sort", w)), dim.Render(clip(" g folder  r refresh  i details  e errors  ? help  q quit", w)))
	content := strings.Join(lines, "\n")
	if m.mode != "" {
		title, body, footer := "", "", "Esc close"
		switch m.mode {
		case "confirm":
			title = "Delete this item?"
			body = safe(m.pending.Path) + "\n\n" + bytes(m.pending.Size) + " · Permanently deleted. No undo."
			inactive := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1)
			active := lipgloss.NewStyle().Foreground(bg).Background(accent).Bold(true).Padding(0, 1)
			no, yes := inactive.Render("  Cancel"), inactive.Render("  Delete")
			if m.confirmYes {
				yes = active.Render("▶ Delete")
			} else {
				no = active.Render("▶ Cancel")
			}
			footer = no + "    " + yes + "\n←/→ choose · Enter confirm · y yes · Esc cancel"
		case "filter", "folder":
			title = map[string]string{"filter": "Filter files", "folder": "Choose folder"}[m.mode]
			body = m.input.View()
			footer = "Enter apply · Esc cancel"
		case "error":
			title = "Deletion incomplete"
			body = safe(m.errorPath) + "\n\n" + safe(m.status)
			footer = "Enter / Esc close · affected folder refreshed"
		case "help":
			title = "Keyboard shortcuts"
			body = "↑↓ / j k  Move\nEnter / Space  Expand or collapse\n→ / ←  Expand or move to parent\nd  Delete highlighted item\ny  Confirm deletion in popup\n/  Filter tree · Esc clears\ns  Sort size / modified / name\ng  Choose folder\nr  Refresh scan explicitly\ni  Full item details\ne  Unreadable locations\nq  Quit\n\nThe tree is cached for this session."
		case "details":
			title = "Item details"
			if len(m.rows) > 0 {
				e := m.rows[m.cursor]
				body = safe(e.Path) + "\n\nSize: " + bytes(e.Size) + "\nModified: " + e.Info.ModTime().Format("2006-01-02 15:04:05") + "\nMode: " + e.Info.Mode().String()
			}
		case "errors":
			title = "Unreadable locations"
			m.failureMu.Lock()
			body = strings.Join(append(append([]string{}, m.data.Errors...), m.failures...), "\n")
			m.failureMu.Unlock()
			if body == "" {
				body = "None"
			}
		}
		pw := min(72, w-6)
		wrapped := strings.Split(ansi.Hardwrap(body, max(10, pw-4), false), "\n")
		limit := max(1, m.height-12)
		start := min(m.modalOffset, max(0, len(wrapped)-limit))
		if len(wrapped) > limit {
			footer += "\n↑/↓ scroll"
		}
		body = strings.Join(wrapped[start:min(len(wrapped), start+limit)], "\n")
		popup := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Background(bg).Foreground(fg).Padding(1, 1).Width(pw).Render(a.Render(title) + "\n\n" + body + "\n\n" + footer)
		content = overlay(content, popup, w, m.height)
	}
	if m.height < 18 || m.width < 45 {
		content = "Sweep\n\nResize terminal to at least 45 × 18.\nq quits."
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func overlay(base, popup string, w, h int) string {
	lines := strings.Split(base, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	panel := strings.Split(popup, "\n")
	pw := lipgloss.Width(popup)
	x := max(0, (w-pw)/2)
	y := max(0, (h-len(panel))/2)
	for i, line := range panel {
		if y+i >= h {
			break
		}
		old := lines[y+i]
		left := ansi.Truncate(old, x, "")
		left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
		right := ansi.Cut(old, x+pw, w)
		lines[y+i] = left + line + right
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
