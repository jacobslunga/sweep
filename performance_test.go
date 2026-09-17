package main

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// A tiny deletion in a large cached scan must not rebuild the entire index.
func BenchmarkDeleteLargeCache(b *testing.B) {
	info, err := os.Stat(".")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		m := newModel("/scan", "/home/user")
		m.data = snapshot{Root: "/scan", Entries: map[string]entry{}}
		m.data.Entries["/scan"] = entry{Path: "/scan", Info: info, Size: 100010}
		for n := 0; n < 100000; n++ {
			p := fmt.Sprintf("/scan/keep/dir-%06d", n)
			m.data.Entries[p] = entry{Path: p, Info: info, Size: 1}
		}
		m.data.Entries["/scan/keep"] = entry{Path: "/scan/keep", Info: info, Size: 100000}
		m.data.Entries["/scan/remove"] = entry{Path: "/scan/remove", Info: info, Size: 10}
		for n := 0; n < 10; n++ {
			p := fmt.Sprintf("/scan/remove/item-%d", n)
			m.data.Entries[p] = entry{Path: p, Info: info, Size: 1}
		}
		m.index()
		m.pending = m.data.Entries["/scan/remove"]
		m.deleteFn = func(context.Context, entry, snapshot, string) error { return nil }
		b.StartTimer()
		cmd := m.startDelete()
		m.Update(cmd())
		b.StopTimer()
		m.workers.Wait()
		m.cancel()
	}
}
