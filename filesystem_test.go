package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0o600); e != nil {
		t.Fatal(e)
	}
}

func TestScanAndDelete(t *testing.T) {
	root := fixture(t)
	dir := filepath.Join(root, "folder")
	if e := os.Mkdir(dir, 0o700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(dir, "a"), "hello")
	write(t, filepath.Join(root, "keep"), "remain")
	s := scan(context.Background(), root)
	if s.Entries[root].Size != 11 {
		t.Fatal(s.Entries[root])
	}
	if e := deleteTarget(context.Background(), s.Entries[dir], s, "/Users/test-user"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("not deleted", e)
	}
	if b, e := os.ReadFile(filepath.Join(root, "keep")); e != nil || string(b) != "remain" {
		t.Fatal("sibling changed")
	}
}

func TestChangedAndAddedFiles(t *testing.T) {
	for _, change := range []string{"modified", "added", "missing"} {
		t.Run(change, func(t *testing.T) {
			root := fixture(t)
			dir := filepath.Join(root, "d")
			os.Mkdir(dir, 0o700)
			p := filepath.Join(dir, "a")
			write(t, p, "before")
			s := scan(context.Background(), root)
			switch change {
			case "modified":
				write(t, p, "after changes")
			case "added":
				write(t, filepath.Join(dir, "b"), "new")
			case "missing":
				os.Remove(p)
			}
			if e := deleteTarget(context.Background(), s.Entries[dir], s, "/Users/test-user"); e == nil {
				t.Fatal("accepted changed directory")
			}
			if _, e := os.Stat(dir); e != nil {
				t.Fatal("directory removed")
			}
		})
	}
}

func TestLinksAndReplacedAncestor(t *testing.T) {
	root := fixture(t)
	outside := fixture(t)
	write(t, filepath.Join(outside, "keep"), "precious")
	link := filepath.Join(root, "link")
	if e := os.Symlink(outside, link); e != nil {
		t.Fatal(e)
	}
	s := scan(context.Background(), root)
	if len(s.Entries) != 2 || s.Entries[root].Size != 0 {
		t.Fatal("followed link")
	}
	if e := deleteTarget(context.Background(), s.Entries[link], s, "/Users/test-user"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(outside, "keep")); e != nil {
		t.Fatal("followed deleted link")
	}
	dir := filepath.Join(root, "dir")
	os.Mkdir(dir, 0o700)
	p := filepath.Join(dir, "keep")
	write(t, p, "local")
	s = scan(context.Background(), root)
	os.Rename(dir, dir+"-old")
	os.Symlink(outside, dir)
	if e := deleteTarget(context.Background(), s.Entries[p], s, "/Users/test-user"); e == nil {
		t.Fatal("accepted symlink ancestor")
	}
}

func TestProtectedCancelAndOverlap(t *testing.T) {
	root := fixture(t)
	p := filepath.Join(root, "a")
	write(t, p, "a")
	s := scan(context.Background(), root)
	if e := deleteTarget(context.Background(), s.Entries[root], s, "/Users/test-user"); e == nil {
		t.Fatal("deleted scan root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !scan(ctx, root).Cancelled {
		t.Fatal("scan ignored cancellation")
	}
	if e := deleteTarget(ctx, s.Entries[p], s, "/Users/test-user"); e == nil {
		t.Fatal("delete ignored cancellation")
	}
	sel := map[string]entry{root: s.Entries[root], p: s.Entries[p]}
	if len(targets(sel)) != 1 {
		t.Fatal("overlap not collapsed")
	}
	for _, p := range []string{"/", "/Users", "/Users/test-user"} {
		if !protected(p, root, "/Users/test-user") {
			t.Fatal("unprotected", p)
		}
	}
}

func TestUnreadable(t *testing.T) {
	root := fixture(t)
	dir := filepath.Join(root, "blocked")
	os.Mkdir(dir, 0o700)
	write(t, filepath.Join(dir, "a"), "x")
	os.Chmod(dir, 0o000)
	defer os.Chmod(dir, 0o700)
	s := scan(context.Background(), root)
	if len(s.Errors) == 0 {
		t.Skip("current user can read permission-restricted directory")
	}
	if !s.Entries[dir].Incomplete || !s.Entries[root].Incomplete {
		t.Fatal("unreadable state not propagated")
	}
	if err := deleteTarget(context.Background(), s.Entries[dir], s, "/Users/test-user"); err == nil {
		t.Fatal("accepted partial directory")
	}
}

func TestMissingTargetFailure(t *testing.T) {
	root := fixture(t)
	p := filepath.Join(root, "a")
	write(t, p, "x")
	s := scan(context.Background(), root)
	os.Remove(p)
	if e := deleteTarget(context.Background(), s.Entries[p], s, "/Users/test-user"); e == nil {
		t.Fatal("missing file reported successful")
	}
}

func TestSafeText(t *testing.T) {
	if strings.ContainsAny(safe("\x1b[31m\nfile"), "\x1b\n") {
		t.Fatal("terminal controls retained")
	}
}

func TestSystemCachesAreNotBlanketBlocked(t *testing.T) {
	for _, p := range []string{"/Library/Caches", "/Library/Caches/com.example.app", "/Users/test-user/Library/Caches"} {
		if protected(p, "/", "/Users/test-user") {
			t.Fatal("cache directory is blocked", p)
		}
	}
	if !protected("/Library", "/Library", "/Users/test-user") {
		t.Fatal("scan root not protected")
	}
}
