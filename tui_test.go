package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func key(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	}
	panic(s)
}

func treeFixture(t *testing.T) *model {
	root := fixture(t)
	dir := filepath.Join(root, "folder")
	os.Mkdir(dir, 0o700)
	write(t, filepath.Join(dir, "first"), "12345")
	write(t, filepath.Join(dir, "second"), "abc")
	m := newModel(root, "/Users/test-user")
	m.data = scan(context.Background(), root)
	m.index()
	t.Cleanup(m.cancel)
	return m
}

func TestTreeExpandCached(t *testing.T) {
	m := treeFixture(t)
	if len(m.rows) != 1 {
		t.Fatal("folder not collapsed")
	}
	m.Update(key("enter"))
	if len(m.rows) != 3 {
		t.Fatal("not expanded")
	}
	if m.scanning || m.generation != 0 {
		t.Fatal("navigation triggered scan")
	}
	m.Update(key("enter"))
	if len(m.rows) != 1 {
		t.Fatal("not collapsed")
	}
	m.Update(key("/"))
	m.input.SetValue("second")
	m.Update(key("enter"))
	if len(m.rows) != 2 || !strings.HasSuffix(m.rows[1].Path, "second") {
		t.Fatal("filter did not expose matching child")
	}
}

func TestDirectDeleteCancel(t *testing.T) {
	m := treeFixture(t)
	m.Update(key("d"))
	if m.mode != "confirm" {
		t.Fatal("d did not open popup")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Delete this item?") {
		t.Fatal("popup absent")
	}
	m.Update(key("enter"))
	if m.deleting || m.mode != "" {
		t.Fatal("cancel must be default")
	}
	m.Update(key("d"))
	m.Update(key("esc"))
	if _, e := os.Stat(m.rows[0].Path); e != nil {
		t.Fatal("cancel deleted target")
	}
}

func TestDeleteUpdatesCacheAndAncestors(t *testing.T) {
	m := treeFixture(t)
	m.Update(key("enter"))
	m.cursor = 1
	p := m.rows[1].Path
	dir := filepath.Dir(p)
	m.Update(key("d"))
	_, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("confirmation did not start deletion")
	}
	m.Update(cmd())
	if m.scanning || m.generation != 0 || m.mode != "" {
		t.Fatal("delete triggered scan or results interruption")
	}
	if _, ok := m.data.Entries[p]; ok {
		t.Fatal("deleted path cached")
	}
	if m.data.Entries[dir].Size != 3 || m.data.Entries[m.root].Size != 3 {
		t.Fatal("ancestor sizes not updated")
	}
	if !m.expanded[dir] {
		t.Fatal("expansion lost")
	}
	// The parent metadata must be updated too, or a second deletion falsely
	// reports the directory as externally changed.
	m.cursor = 0
	m.Update(key("d"))
	_, cmd = m.Update(key("y"))
	m.Update(cmd())
	if m.mode == "error" || len(m.rows) != 0 {
		t.Fatal("cached ancestor could not be deleted", m.status)
	}
}

func TestChangedTargetLocalRefresh(t *testing.T) {
	m := treeFixture(t)
	dir := m.rows[0].Path
	m.Update(key("d"))
	write(t, filepath.Join(dir, "new"), "new")
	_, cmd := m.Update(key("y"))
	m.Update(cmd())
	if m.mode != "error" || m.scanning {
		t.Fatal("failure not reported locally")
	}
	if _, ok := m.data.Entries[filepath.Join(dir, "new")]; !ok {
		t.Fatal("failed subtree not refreshed")
	}
	if m.data.Entries[m.root].Size != 11 {
		t.Fatal("failed subtree totals wrong")
	}
}

func TestMouseLayoutAndNoMotto(t *testing.T) {
	m := treeFixture(t)
	m.Update(tea.MouseClickMsg{X: 4, Y: 8, Button: tea.MouseLeft})
	if len(m.rows) != 3 {
		t.Fatal("mouse did not expand")
	}
	for _, size := range [][2]int{{110, 32}, {60, 22}, {45, 18}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		text := ansi.Strip(m.View().Content)
		if !strings.Contains(text, "system:") || strings.Contains(text, "WORKSPACE") || strings.Contains(text, "make room") {
			t.Fatal("wrong header")
		}
		if len(strings.Split(text, "\n")) > size[1] {
			t.Fatal("view overflow")
		}
		m.Update(key("d"))
		view := m.View().Content
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("popup width overflow", size, line)
			}
		}
		m.Update(key("esc"))
	}
}

func TestStaleScanIgnored(t *testing.T) {
	m := treeFixture(t)
	m.generation = 3
	m.Update(scanMsg{snapshot: snapshot{Root: "wrong"}, ID: 2})
	if m.data.Root == "wrong" {
		t.Fatal("stale scan applied")
	}
}

func TestExplicitRefresh(t *testing.T) {
	m := treeFixture(t)
	_, cmd := m.Update(key("r"))
	if cmd == nil || !m.scanning || m.generation != 1 {
		t.Fatal("explicit refresh missing")
	}
	m.Update(cmd())
	if m.scanning {
		t.Fatal("refresh did not finish")
	}
}

func TestConcurrentDeletionAndGracefulQuit(t *testing.T) {
	m := treeFixture(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	m.deleteFn = func(ctx context.Context, e entry, s snapshot, home string) error {
		started <- struct{}{}
		<-release
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return deleteTarget(ctx, e, s, home)
	}
	// Always release workers before temporary directories are cleaned up.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		m.workers.Wait()
	}()
	m.Update(key("enter"))
	m.cursor = 1
	first := m.rows[1].Path
	m.Update(key("d"))
	_, one := m.Update(key("y"))
	if len(m.rows) != 2 {
		t.Fatal("target did not disappear immediately")
	}
	if _, ok := m.data.Entries[first]; ok {
		t.Fatal("target still in cache")
	}
	<-started
	// A parent of an active target cannot be queued concurrently.
	m.cursor = 0
	m.Update(key("d"))
	if m.mode == "confirm" {
		t.Fatal("overlapping parent accepted")
	}
	m.cursor = 1
	m.Update(key("d"))
	_, two := m.Update(key("y"))
	<-started
	if len(m.jobs) != 2 || len(m.rows) != 1 {
		t.Fatal("second deletion not accepted")
	}
	m.Update(key("r"))
	if m.scanning {
		t.Fatal("refresh raced with deletion")
	}
	_, quit := m.Update(shutdownMsg{})
	if quit != nil || !m.quitting {
		t.Fatal("quit did not drain jobs")
	}
	// Repeated Ctrl+C/SIGINT cannot cut the drain short.
	_, quit = m.Update(shutdownMsg{})
	if quit != nil {
		t.Fatal("second interrupt forced exit")
	}
	close(release)
	// Apply completions out of order to exercise concurrent cache accounting.
	_, quit = m.Update(two())
	if quit != nil {
		t.Fatal("exited before all jobs completed")
	}
	_, quit = m.Update(one())
	if quit == nil {
		t.Fatal("did not exit when last job completed")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("not a quit command")
	}
	if m.data.Entries[m.root].Size != 0 || len(m.jobs) != 0 {
		t.Fatal("pending sizes or jobs incorrect")
	}
}

func TestBackgroundFailureRestoresItem(t *testing.T) {
	m := treeFixture(t)
	path := m.rows[0].Path
	m.deleteFn = func(context.Context, entry, snapshot, string) error { return os.ErrPermission }
	m.Update(key("d"))
	_, cmd := m.Update(key("y"))
	if len(m.rows) != 0 {
		t.Fatal("not hidden immediately")
	}
	m.Update(cmd())
	m.workers.Wait()
	if len(m.rows) != 1 || m.rows[0].Path != path || m.data.Entries[m.root].Size != 8 {
		t.Fatal("failed item not restored")
	}
	if len(m.failures) != 1 || m.mode != "error" {
		t.Fatal("failure not reported")
	}
}
