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

// Group is paths queued by one launch, with the subtitle language its menu
// entry asked for ("" for the configured languages) and the job: "" to
// download subtitles, "sync" to re-sync subtitle files.
type Group struct {
	Lang  string
	Mode  string
	Paths []string
}

// Add records paths for whichever process handles the queue.
func (q *Queue) Add(lang, mode string, paths []string) error {
	if err := os.MkdirAll(q.Dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d.path", time.Now().UnixNano(), os.Getpid())
	tmp := filepath.Join(q.Dir, name+".tmp")
	body := "lang=" + lang + "\nmode=" + mode + "\n" + strings.Join(paths, "\n")
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
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
// then removes and returns all queued paths, grouped by language in the
// order the languages were first queued.
func (q *Queue) Drain(quiet, max time.Duration) []Group {
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
	var groups []Group
	index := map[string]int{}
	for _, e := range q.entries() {
		b, err := os.ReadFile(e)
		os.Remove(e)
		if err != nil {
			continue
		}
		lang, mode := "", ""
		for i, p := range strings.Split(string(b), "\n") {
			p = strings.TrimSpace(p)
			if i < 2 && strings.HasPrefix(p, "lang=") {
				lang = strings.TrimPrefix(p, "lang=")
				continue
			}
			if i < 2 && strings.HasPrefix(p, "mode=") {
				mode = strings.TrimPrefix(p, "mode=")
				continue
			}
			if p == "" {
				continue
			}
			key := lang + "|" + mode
			gi, ok := index[key]
			if !ok {
				gi = len(groups)
				index[key] = gi
				groups = append(groups, Group{Lang: lang, Mode: mode})
			}
			groups[gi].Paths = append(groups[gi].Paths, p)
		}
	}
	return groups
}
