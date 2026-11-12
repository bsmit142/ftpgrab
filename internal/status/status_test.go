package status

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crazy-max/ftpgrab/v7/internal/journal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNilTracker(t *testing.T) {
	var tr *Tracker
	tr.StartRun()
	tr.SetQueue([]Item{{File: "/a", Size: 1}})
	transfer := tr.Begin("/a", 10)
	require.NotNil(t, transfer)
	_, err := transfer.Writer(&bytes.Buffer{}).Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, int64(5), transfer.View().Done)
	tr.End(transfer, &journal.Entry{Level: journal.EntryLevelSuccess})
	tr.EndRun(1, 0, 0, "")
	tr.SetNextRun(time.Now())
}

func TestRunLifecycle(t *testing.T) {
	tr := New("v1")
	assert.Equal(t, "idle", tr.Snapshot().Phase)

	tr.StartRun()
	assert.Equal(t, "listing", tr.Snapshot().Phase)

	tr.SetQueue([]Item{{File: "/a", Size: 100}, {File: "/b", Size: 50}})
	s := tr.Snapshot()
	assert.Equal(t, "downloading", s.Phase)
	assert.Equal(t, int64(150), s.Remaining)
	assert.Equal(t, ItemPending, s.Queue[0].State)

	transfer := tr.Begin("/a", 100)
	assert.Equal(t, 1, transfer.Index)
	assert.Equal(t, 2, transfer.Total)
	var buf bytes.Buffer
	_, err := transfer.Writer(&buf).Write(make([]byte, 40))
	require.NoError(t, err)

	s = tr.Snapshot()
	require.NotNil(t, s.Current)
	assert.Equal(t, int64(40), s.Current.Done)
	assert.InDelta(t, 40.0, s.Current.Percent, 0.001)
	assert.Equal(t, ItemActive, s.Queue[0].State)
	assert.Equal(t, int64(60+50), s.Remaining)
	assert.InDelta(t, -1.0, s.ETA, 0.001) // no speed sample yet

	transfer.Sample(transfer.Started.Add(2 * time.Second)) // 40 bytes in 2s
	s = tr.Snapshot()
	assert.InDelta(t, 20.0, s.Current.Speed, 0.001)
	assert.InDelta(t, 3.0, s.Current.ETA, 0.001) // 60 bytes left
	assert.InDelta(t, 5.5, s.ETA, 0.001)         // 110 bytes left in the run

	tr.End(transfer, &journal.Entry{Level: journal.EntryLevelSuccess, Text: "ok"})
	s = tr.Snapshot()
	assert.Nil(t, s.Current)
	assert.Equal(t, ItemSuccess, s.Queue[0].State)
	require.Len(t, s.Recent, 1)
	assert.Equal(t, "/a", s.Recent[0].File)
	assert.Equal(t, "success", s.Recent[0].Level)

	tr.EndRun(1, 0, 3, "")
	s = tr.Snapshot()
	assert.Equal(t, "idle", s.Phase)
	require.NotNil(t, s.Run)
	assert.Equal(t, 3, s.Run.Skip)
	assert.False(t, s.Run.Finished.IsZero())
}

func TestFailedEntryMarksQueue(t *testing.T) {
	tr := New("v1")
	tr.StartRun()
	tr.SetQueue([]Item{{File: "/a", Size: 1}})
	transfer := tr.Begin("/a", 1)
	tr.End(transfer, &journal.Entry{Level: journal.EntryLevelError, Text: "boom"})
	s := tr.Snapshot()
	assert.Equal(t, ItemError, s.Queue[0].State)
	assert.Equal(t, "boom", s.Recent[0].Text)
}

func TestRecentIsCapped(t *testing.T) {
	tr := New("v1")
	for range maxRecent + 5 {
		tr.End(tr.Begin("/a", 1), &journal.Entry{Level: journal.EntryLevelSuccess})
	}
	assert.Len(t, tr.Snapshot().Recent, maxRecent)
}

// Run with -race: the page reads while a download writes and samples.
func TestConcurrentAccess(t *testing.T) {
	tr := New("v1")
	tr.StartRun()
	tr.SetQueue([]Item{{File: "/a", Size: 1 << 20}})
	transfer := tr.Begin("/a", 1<<20)
	w := transfer.Writer(io.Discard)
	h := tr.Handler()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_, _ = w.Write(make([]byte, 1024))
			transfer.Sample(time.Now())
		}
	}()
	for range 200 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/status", nil))
	}
	<-done
	assert.Equal(t, int64(1000*1024), transfer.View().Done)
}

func TestHandler(t *testing.T) {
	tr := New("v1")
	tr.StartRun()
	tr.SetQueue([]Item{{File: "/a", Size: 1}})
	h := tr.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var s Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s))
	assert.Equal(t, "v1", s.Version)
	assert.Equal(t, "downloading", s.Phase)
	require.Len(t, s.Queue, 1)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<title>FTPGrab</title>")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
