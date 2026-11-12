// Package status tracks what ftpgrab is doing, for progress logs and the
// status page.
package status

import (
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crazy-max/ftpgrab/v7/internal/journal"
)

// maxRecent is how many finished downloads the status page keeps.
const maxRecent = 100

// ItemState is the state of a file in the current run's queue
type ItemState string

const (
	ItemPending = ItemState("pending")
	ItemActive  = ItemState("active")
	ItemSuccess = ItemState("success")
	ItemWarning = ItemState("warning")
	ItemError   = ItemState("error")
)

// Item is a file the current run will download
type Item struct {
	File  string    `json:"file"`
	Size  int64     `json:"size"`
	State ItemState `json:"state"`
}

// Result is a finished download
type Result struct {
	File     string    `json:"file"`
	Size     int64     `json:"size"`
	Level    string    `json:"level"`
	Text     string    `json:"text"`
	Finished time.Time `json:"finished"`
	Seconds  float64   `json:"seconds"`
}

// Run summarises a run
type Run struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Success  int       `json:"success"`
	Error    int       `json:"error"`
	Skip     int       `json:"skip"`
	Failure  string    `json:"failure,omitempty"`
}

// Tracker holds the current state. A nil Tracker is valid and records nothing.
type Tracker struct {
	mu       sync.RWMutex
	version  string
	running  bool
	run      *Run
	nextRun  time.Time
	queue    []Item
	queueSet bool
	current  *Transfer
	recent   []Result
}

// New creates a tracker
func New(version string) *Tracker {
	return &Tracker{version: version}
}

// StartRun marks the start of a run
func (t *Tracker) StartRun() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = true
	t.run = &Run{Started: time.Now()}
	t.queue, t.queueSet, t.current = nil, false, nil
}

// EndRun marks the end of a run with its counts, and why it failed if it did
func (t *Tracker) EndRun(success, errors, skip int, failure string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	t.current = nil
	if t.run != nil {
		t.run.Finished = time.Now()
		t.run.Success, t.run.Error, t.run.Skip = success, errors, skip
		t.run.Failure = failure
	}
}

// SetNextRun records when the scheduler runs next
func (t *Tracker) SetNextRun(next time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextRun = next
}

// SetQueue records the files this run will download
func (t *Tracker) SetQueue(items []Item) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range items {
		items[i].State = ItemPending
	}
	t.queue, t.queueSet = items, true
}

// Begin starts tracking a download; it works on a nil Tracker too.
func (t *Tracker) Begin(file string, size int64) *Transfer {
	now := time.Now()
	tr := &Transfer{File: file, Size: size, Started: now, lastTime: now}
	if t == nil {
		return tr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tr.Total = len(t.queue)
	for i := range t.queue {
		if t.queue[i].File == file {
			t.queue[i].State = ItemActive
			tr.Index = i + 1
		}
	}
	t.current = tr
	return tr
}

// End records the outcome of a download
func (t *Tracker) End(tr *Transfer, entry *journal.Entry) {
	if t == nil || tr == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := ItemError
	switch entry.Level {
	case journal.EntryLevelSuccess:
		state = ItemSuccess
	case journal.EntryLevelWarning:
		state = ItemWarning
	}
	for i := range t.queue {
		if t.queue[i].File == tr.File {
			t.queue[i].State = state
		}
	}
	if t.current == tr {
		t.current = nil
	}
	t.recent = slices.Insert(t.recent, 0, Result{
		File:     tr.File,
		Size:     tr.Size,
		Level:    string(entry.Level),
		Text:     entry.Text,
		Finished: time.Now(),
		Seconds:  time.Since(tr.Started).Seconds(),
	})
	if len(t.recent) > maxRecent {
		t.recent = t.recent[:maxRecent]
	}
}

// Transfer is a download in progress
type Transfer struct {
	File    string
	Size    int64
	Started time.Time
	Index   int // position in the queue, 0 if unknown
	Total   int // queue length

	done atomic.Int64

	mu        sync.Mutex
	speed     float64 // bytes per second, smoothed
	lastBytes int64
	lastTime  time.Time
}

// Writer counts the bytes written through w
func (tr *Transfer) Writer(w io.Writer) io.Writer {
	return &countingWriter{w: w, tr: tr}
}

type countingWriter struct {
	w  io.Writer
	tr *Transfer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.tr.done.Add(int64(n))
	return n, err
}

// Sample updates the smoothed speed; call it about once a second.
func (tr *Transfer) Sample(now time.Time) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	done := tr.done.Load()
	if dt := now.Sub(tr.lastTime).Seconds(); dt > 0 {
		rate := float64(done-tr.lastBytes) / dt
		if tr.speed == 0 {
			tr.speed = rate
		} else {
			tr.speed = 0.2*rate + 0.8*tr.speed // ~5s time constant
		}
	}
	tr.lastBytes, tr.lastTime = done, now
}

// TransferView is a point-in-time copy of a Transfer
type TransferView struct {
	File    string    `json:"file"`
	Size    int64     `json:"size"`
	Done    int64     `json:"done"`
	Percent float64   `json:"percent"`
	Speed   float64   `json:"speed"`
	ETA     float64   `json:"eta"` // seconds, -1 if unknown
	Started time.Time `json:"started"`
	Index   int       `json:"index"`
	Total   int       `json:"total"`
}

// View returns the transfer's current figures
func (tr *Transfer) View() TransferView {
	tr.mu.Lock()
	speed := tr.speed
	tr.mu.Unlock()
	done := tr.done.Load()
	v := TransferView{
		File: tr.File, Size: tr.Size, Done: done, Speed: speed, ETA: -1,
		Started: tr.Started, Index: tr.Index, Total: tr.Total,
	}
	if tr.Size > 0 {
		v.Percent = min(100, float64(done)*100/float64(tr.Size))
	}
	if speed > 0 {
		v.ETA = float64(max(0, tr.Size-done)) / speed
	}
	return v
}

// Snapshot is the status page's JSON
type Snapshot struct {
	Version   string        `json:"version"`
	Now       time.Time     `json:"now"`
	Phase     string        `json:"phase"` // idle, listing or downloading
	NextRun   time.Time     `json:"nextRun,omitzero"`
	Run       *Run          `json:"run,omitempty"`
	Current   *TransferView `json:"current,omitempty"`
	Queue     []Item        `json:"queue"`
	Remaining int64         `json:"remaining"` // bytes left in this run
	ETA       float64       `json:"eta"`       // seconds for the whole queue, -1 if unknown
	Recent    []Result      `json:"recent"`
}

// Snapshot returns a copy of the current state
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := Snapshot{
		Version: t.version,
		Now:     time.Now(),
		Phase:   "idle",
		NextRun: t.nextRun,
		Queue:   slices.Clone(t.queue),
		Recent:  slices.Clone(t.recent),
		ETA:     -1,
	}
	if s.Queue == nil {
		s.Queue = []Item{}
	}
	if s.Recent == nil {
		s.Recent = []Result{}
	}
	if t.run != nil {
		run := *t.run
		s.Run = &run
	}
	if t.running {
		s.Phase = "downloading"
		if !t.queueSet {
			s.Phase = "listing"
		}
	}
	for _, item := range t.queue {
		if item.State == ItemPending {
			s.Remaining += item.Size
		}
	}
	if t.current != nil {
		v := t.current.View()
		s.Current = &v
		s.Remaining += max(0, v.Size-v.Done)
		if v.Speed > 0 {
			s.ETA = float64(s.Remaining) / v.Speed
		}
	}
	return s
}
