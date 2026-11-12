package grabber

import (
	"fmt"
	"sync"
	"time"

	"github.com/crazy-max/ftpgrab/v7/internal/status"
	"github.com/docker/go-units"
	"github.com/rs/zerolog"
)

// watchProgress samples the transfer's speed every second for the status
// page, and logs its progress every download.progressInterval. Call the
// returned func once the transfer ends.
func (c *Client) watchProgress(logger zerolog.Logger, tr *status.Transfer) (stop func()) {
	var interval time.Duration
	if c.config != nil && c.config.ProgressInterval != nil {
		interval = *c.config.ProgressInterval
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		lastLog := tr.Started
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				tr.Sample(now)
				if interval <= 0 || now.Sub(lastLog) < interval {
					continue
				}
				lastLog = now
				v := tr.View()
				ev := logger.Info().
					Str("progress", fmt.Sprintf("%.0f%%", v.Percent)).
					Str("done", units.HumanSize(float64(v.Done))).
					Str("speed", units.HumanSize(v.Speed)+"/s")
				if v.ETA >= 0 {
					ev = ev.Str("eta", (time.Duration(v.ETA) * time.Second).String())
				}
				ev.Msg("Downloading")
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
		})
	}
}
