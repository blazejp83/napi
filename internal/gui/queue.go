//go:build windows

package gui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/blazejp83/napi/internal/app"
	"github.com/blazejp83/napi/internal/batch"
	"github.com/blazejp83/napi/internal/winui"
)

// Job is one piece of work in the card's queue: one Explorer launch (one
// selection) or one napiprojekt: link.
type Job struct {
	ID      int    `json:"id"`
	Kind    string `json:"kind"`    // get, sync, link
	Lang    string `json:"lang"`    // get: "" for the configured languages, or ENG
	Label   string `json:"label"`   // what it is about, for the list
	Status  string `json:"status"`  // queued, running, done
	Outcome string `json:"outcome"` // done: ok, partial, none, error, cancelled, kept
	Summary string `json:"summary"` // done: one line about the result

	paths  []string
	result jobResult
}

// autoCloseSeconds is how long the card stays after everything went well.
const autoCloseSeconds = 4

func jobsFor(groups []winui.Group) []*Job {
	var jobs []*Job
	for _, g := range groups {
		switch g.Mode {
		case "link":
			for _, hash := range g.Paths { // each click is its own job
				jobs = append(jobs, &Job{Kind: "link", Label: "napiprojekt:" + hash[:min(8, len(hash))] + "…", paths: []string{hash}})
			}
		case "sync":
			jobs = append(jobs, &Job{Kind: "sync", Label: pathsLabel(g.Paths), paths: g.Paths})
		default:
			jobs = append(jobs, &Job{Kind: "get", Lang: g.Lang, Label: pathsLabel(g.Paths), paths: g.Paths})
		}
	}
	return jobs
}

func pathsLabel(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	l := filepath.Base(paths[0])
	if len(paths) > 1 {
		l += fmt.Sprintf(" + %d", len(paths)-1)
	}
	return l
}

// enqueue adds jobs for groups and wakes the worker.
func (a *App) enqueue(groups []winui.Group) {
	jobs := jobsFor(groups)
	if len(jobs) == 0 {
		return
	}
	a.mu.Lock()
	for _, j := range jobs {
		a.nextID++
		j.ID, j.Status = a.nextID, "queued"
		a.jobs = append(a.jobs, j)
	}
	a.mu.Unlock()
	if a.ctx != nil {
		a.publishJobs()
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// publishJobs sends the queue to the frontend.
func (a *App) publishJobs() {
	a.mu.Lock()
	list := make([]Job, len(a.jobs))
	for i, j := range a.jobs {
		list[i] = *j
	}
	a.mu.Unlock()
	a.setState(func(s *PanelState) { s.Jobs = list })
}

func (a *App) relabel(j *Job, label string) {
	a.mu.Lock()
	j.Label = label
	a.mu.Unlock()
	a.publishJobs()
}

// RemoveJob drops a job that has not started yet.
func (a *App) RemoveJob(id int) {
	a.mu.Lock()
	for i, j := range a.jobs {
		if j.ID == id && j.Status == "queued" {
			a.jobs = append(a.jobs[:i], a.jobs[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	a.publishJobs()
	select { // the queue may now be finished
	case a.wake <- struct{}{}:
	default:
	}
}

// pollQueue brings in menu launches and links that arrive while the card is
// open.
func (a *App) pollQueue(ctx context.Context) {
	for ctx.Err() == nil {
		if groups := a.poll(); len(groups) > 0 {
			a.enqueue(groups)
			continue
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

// next marks the first queued job as running and returns it.
func (a *App) next(ctx context.Context) (*Job, context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.jobs {
		if j.Status == "queued" {
			j.Status = "running"
			jobCtx, cancel := context.WithCancel(ctx)
			a.jobCancel = cancel
			return j, jobCtx
		}
	}
	return nil, nil
}

// runQueue works through the jobs one at a time; when the queue runs dry it
// shows the outcome and waits for more.
func (a *App) runQueue(ctx context.Context) {
	idle := false
	for ctx.Err() == nil {
		j, jobCtx := a.next(ctx)
		if j == nil {
			if !idle {
				idle = true
				a.showFinal()
			}
			select {
			case <-a.wake:
			case <-ctx.Done():
				return
			}
			continue
		}
		idle = false
		a.publishJobs()
		a.setState(func(s *PanelState) {
			s.Phase, s.Title, s.Detail, s.Done, s.Total, s.Busy, s.Choices, s.Close = "running", "Przygotowuję…", j.Label, 0, 0, false, nil, 0
		})
		var res jobResult
		switch j.Kind {
		case "link":
			res = a.linkJob(jobCtx, j)
		case "sync":
			res = outcome(a.syncRound(jobCtx, j.paths, 0))
		default:
			res = a.getJob(jobCtx, j)
		}
		app.Remember(res.results)
		a.mu.Lock()
		a.jobCancel()
		a.jobCancel = nil
		j.Status, j.Outcome, j.Summary, j.result = "done", res.kind, res.msg, res
		a.mu.Unlock()
		a.publishJobs()
	}
}

// showFinal shows how the queue went: one job as itself, several as a
// summary with the per-job list. A clean success closes after a few seconds.
func (a *App) showFinal() {
	a.mu.Lock()
	var done []*Job
	for _, j := range a.jobs {
		if j.Status == "done" && j.Outcome != "kept" {
			done = append(done, j)
		}
	}
	a.mu.Unlock()
	if len(done) == 0 {
		a.Close() // nothing was done: the user kept existing subtitles or picked no file
		return
	}
	var res jobResult
	if len(done) == 1 {
		res = done[0].result
	} else {
		var all []batch.Result
		worst := "ok"
		for _, j := range done {
			all = append(all, j.result.results...)
			if severity[j.Outcome] > severity[worst] {
				worst = j.Outcome
			}
		}
		res = outcome(all)
		res.kind = worst
		if (worst == "none" || worst == "cancelled") && severity[best(done)] == 0 {
			res.kind = "partial" // some jobs went well
		}
		res.msg = fmt.Sprintf("Zadania: %d. %s", len(done), res.msg)
	}
	a.finish(res.results, res.kind, res.msg, res.output)
	logPath := ""
	if len(res.results) > 0 {
		logPath = app.WriteRunLog(res.results)
	}
	a.setState(func(s *PanelState) {
		s.Log = logPath
		s.Done, s.Total = len(res.results), len(res.results)
		if res.kind == "ok" {
			s.Close = autoCloseSeconds
		}
	})
}

var severity = map[string]int{"ok": 0, "partial": 1, "none": 2, "cancelled": 3, "error": 4}

// best is the best outcome among jobs.
func best(jobs []*Job) string {
	b := "error"
	for _, j := range jobs {
		if severity[j.Outcome] < severity[b] {
			b = j.Outcome
		}
	}
	return b
}
