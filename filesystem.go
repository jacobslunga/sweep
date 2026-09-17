package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type entry struct {
	Path       string
	Info       fs.FileInfo
	Size       int64
	Incomplete bool
}
type snapshot struct {
	Root      string
	Entries   map[string]entry
	Errors    []string
	Cancelled bool
}

func within(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func scan(ctx context.Context, root string) snapshot {
	s := snapshot{Root: root, Entries: map[string]entry{}}
	badPaths := []string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", p, err))
			badPaths = append(badPaths, p)
			if e, ok := s.Entries[p]; ok {
				e.Incomplete = true
				s.Entries[p] = e
			}
			return nil
		}
		info, e := d.Info()
		if e != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", p, e))
			badPaths = append(badPaths, p)
			return nil
		}
		size := int64(0)
		if info.Mode().IsRegular() {
			size = info.Size()
		}
		s.Entries[p] = entry{Path: p, Info: info, Size: size}
		return nil
	})
	s.Cancelled = errors.Is(err, context.Canceled)
	paths := make([]string, 0, len(s.Entries))
	for p := range s.Entries {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, p := range paths {
		if p == root {
			continue
		}
		e := s.Entries[p]
		parent := filepath.Dir(p)
		if pe, ok := s.Entries[parent]; ok {
			pe.Size += e.Size
			pe.Incomplete = pe.Incomplete || e.Incomplete
			s.Entries[parent] = pe
		}
	}
	// Mark ancestors in O(depth) per error, even when a child could not be statted.
	for _, p := range badPaths {
		for within(root, p) {
			if e, ok := s.Entries[p]; ok {
				e.Incomplete = true
				s.Entries[p] = e
			}
			if p == root {
				break
			}
			p = filepath.Dir(p)
		}
	}
	return s
}

func targets(selected map[string]entry) []entry {
	paths := make([]string, 0, len(selected))
	for p := range selected {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := []entry{}
	for _, p := range paths {
		covered := false
		for _, e := range out {
			if within(e.Path, p) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, selected[p])
		}
	}
	return out
}

func protected(p, root, home string) bool {
	// Trust filesystem permissions rather than a blanket system-directory list.
	// Keep the scan anchor and the user's home (and its ancestors) intact.
	return p == "/" || p == root || within(p, home)
}

func same(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Bind every directory component to a descriptor with O_NOFOLLOW. Operations
// remain confined to the opened parent even if an ancestor is renamed.
func openParent(p string) (*os.Root, error) {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(p), "/"), "/") {
		if part == "" {
			continue
		}
		next, e := openDirectoryAt(fd, part)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	// OpenRoot through the descriptor preserves the directory binding on macOS.
	r, e := os.OpenRoot(fmt.Sprintf("/dev/fd/%d", fd))
	syscall.Close(fd)
	return r, e
}

func deleteTarget(ctx context.Context, e entry, original snapshot, home string) error {
	if !within(original.Root, e.Path) || protected(e.Path, original.Root, home) || e.Incomplete || original.Cancelled {
		return errors.New("protected or incompletely scanned target")
	}
	parent, err := openParent(e.Path)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(e.Path)
	now, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !same(e.Info, now) {
		return errors.New("target changed; rescan before deleting")
	}
	if e.Info.IsDir() {
		// Verify every descendant before starting; a new, changed or missing item
		// invalidates the entire target. Never recursively remove unreviewed entries.
		current := scan(ctx, e.Path)
		if current.Cancelled || len(current.Errors) > 0 {
			return errors.New("cannot fully revalidate directory")
		}
		expected := 0
		for p, old := range original.Entries {
			if within(e.Path, p) {
				expected++
				fresh, ok := current.Entries[p]
				if !ok || !same(old.Info, fresh.Info) {
					return errors.New("directory contents changed; rescan")
				}
			}
		}
		if expected != len(current.Entries) {
			return errors.New("directory contents changed; rescan")
		}
		paths := make([]string, 0, len(current.Entries))
		for p := range current.Entries {
			paths = append(paths, p)
		}
		sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
		for _, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			old := current.Entries[p]
			pr, err := openParent(p)
			if err != nil {
				return err
			}
			info, err := pr.Lstat(filepath.Base(p))
			if err == nil && !same(old.Info, info) && !old.Info.IsDir() {
				err = errors.New("file changed during cleanup")
			}
			if err == nil && old.Info.IsDir() && !os.SameFile(old.Info, info) {
				err = errors.New("directory replaced during cleanup")
			}
			if err == nil {
				err = pr.Remove(filepath.Base(p))
			}
			pr.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return parent.Remove(name)
}

func capacity(path string) (total, free uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) == nil {
		return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
	}
	return
}
