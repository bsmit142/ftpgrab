package grabber

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazy-max/ftpgrab/v7/internal/config"
	"github.com/crazy-max/ftpgrab/v7/internal/db"
	"github.com/crazy-max/ftpgrab/v7/internal/server"
	"github.com/crazy-max/ftpgrab/v7/internal/status"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contentServer serves files from memory; paths in fail always error.
type contentServer struct {
	stubServerHandler
	content map[string]string
	fail    map[string]bool
	calls   map[string]int
}

func (s *contentServer) Retrieve(path string, dest io.Writer) error {
	s.calls[path]++
	if s.fail[path] {
		return errors.New("connection reset")
	}
	_, err := io.WriteString(dest, s.content[path])
	return err
}

func TestGrabTracksStatus(t *testing.T) {
	output := t.TempDir()
	dbCli, err := db.New(nil)
	require.NoError(t, err)

	cfg := (&config.Download{}).GetDefaults()
	cfg.Output = output
	cfg.Retry = 2

	srv := &contentServer{
		content: map[string]string{"/src/new.mkv": "hello world"},
		fail:    map[string]bool{"/src/broken.mkv": true},
		calls:   map[string]int{},
	}
	tracker := status.New("test")
	client := &Client{
		config: cfg,
		db:     dbCli,
		server: &server.Client{Handler: srv},
		status: tracker,
	}

	// Already on disk with the right size, so skipped and not queued.
	require.NoError(t, os.WriteFile(filepath.Join(output, "have.mkv"), []byte("abc"), 0o644))

	now := time.Now()
	files := []File{
		{Base: "/src", SrcDir: "/src", DestDir: output, Info: stubFileInfo{name: "have.mkv", size: 3, modTime: now}},
		{Base: "/src", SrcDir: "/src", DestDir: output, Info: stubFileInfo{name: "new.mkv", size: 11, modTime: now}},
		{Base: "/src", SrcDir: "/src", DestDir: output, Info: stubFileInfo{name: "broken.mkv", size: 5, modTime: now}},
	}

	tracker.StartRun()
	jnl := client.Grab(files)
	tracker.EndRun(jnl.Count.Success, jnl.Count.Error, jnl.Count.Skip, "")

	got, err := os.ReadFile(filepath.Join(output, "new.mkv"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(got))
	assert.Equal(t, 2, srv.calls["/src/broken.mkv"], "retried")

	s := tracker.Snapshot()
	assert.Equal(t, "idle", s.Phase)
	require.Len(t, s.Queue, 2)
	assert.Equal(t, "/src/new.mkv", s.Queue[0].File)
	assert.Equal(t, status.ItemSuccess, s.Queue[0].State)
	assert.Equal(t, status.ItemError, s.Queue[1].State)

	// One result per file, not per attempt; newest first.
	require.Len(t, s.Recent, 2)
	assert.Equal(t, "/src/broken.mkv", s.Recent[0].File)
	assert.Equal(t, "error", s.Recent[0].Level)
	assert.Equal(t, "/src/new.mkv", s.Recent[1].File)
	assert.Equal(t, "success", s.Recent[1].Level)
	assert.Equal(t, int64(11), s.Recent[1].Size)

	assert.Equal(t, 1, s.Run.Success)
	assert.Equal(t, 1, s.Run.Error)
	assert.Equal(t, 1, s.Run.Skip)
}

func TestWatchProgressStops(t *testing.T) {
	client := &Client{config: &config.Download{ProgressInterval: new(time.Duration(0))}}
	tr := (*status.Tracker)(nil).Begin("/a", 10)
	stop := client.watchProgress(zerolog.Nop(), tr)
	stop()
	stop() // safe to call twice
}
