package winui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Queue is a spool folder where every menu launch drops its path. Explorer
// starts one process per selected file; one of them (the lock holder)
// drains the queue and handles all paths in a single run.
type Queue struct{ Dir string }

func NewQueue() *Queue { return &Queue{Dir: filepath.Join(os.TempDir(), "napi-queue")} }

// Add records paths for whichever process handles the queue.
func (q *Queue) Add(paths []string) error {
	if err := os.MkdirAll(q.Dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d.path", time.Now().UnixNano(), os.Getpid())
	tmp := filepath.Join(q.Dir, name+".tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(paths, "\n")), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(q.Dir, name))
}

func (q *Queue) entries() []string {
	m, _ := filepath.Glob(filepath.Join(q.Dir, "*.path"))
	return m
}

// Pending reports whether any paths are waiting.
func (q *Queue) Pending() bool { return len(q.entries()) > 0 }

// Drain waits until no new launches arrive for quiet (at most max overall),
// then removes and returns all queued paths.
func (q *Queue) Drain(quiet, max time.Duration) []string {
	deadline := time.Now().Add(max)
	last, lastChange := -1, time.Now()
	for time.Now().Before(deadline) {
		n := len(q.entries())
		if n != last {
			last, lastChange = n, time.Now()
		} else if time.Since(lastChange) >= quiet {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var paths []string
	for _, e := range q.entries() {
		b, err := os.ReadFile(e)
		os.Remove(e)
		if err != nil {
			continue
		}
		for _, p := range strings.Split(string(b), "\n") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths
}
