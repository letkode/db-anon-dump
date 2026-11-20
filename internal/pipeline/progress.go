package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/letkode/db-anon-dump/internal/engine"
	"github.com/letkode/db-anon-dump/internal/runlog"
	"github.com/letkode/db-anon-dump/internal/tempdb"
)

// every is the heartbeat: how often a long step says it is still working.
func (o *Options) every() time.Duration {
	if o.Heartbeat > 0 {
		return o.Heartbeat
	}
	return 10 * time.Second
}

// vlogf logs only with --verbose.
func (o *Options) vlogf(format string, a ...any) {
	if o.Verbose {
		o.logf(format, a...)
	}
}

func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	case n >= k:
		return fmt.Sprintf("%.1f KB", float64(n)/k)
	}
	return fmt.Sprintf("%d B", n)
}

// stream builds the progress display of a "dump the source into the temporary database" copy: one line per
// table as it starts, and a heartbeat while a table takes long. sizes (optional, bytes per table) turn the
// table counter into an approximate percentage.
func (o *Options) stream(d engine.Dialect, label string, total int, sizes map[string]int64) *tempdb.StreamOptions {
	var all, done int64
	for _, s := range sizes {
		all += s
	}
	prev := ""
	return &tempdb.StreamOptions{
		Marker: d.StreamMarker(), Total: total, Every: o.every(),
		OnTable: func(name string, n int) {
			done += sizes[prev]
			prev = name
			if all > 0 {
				o.logf("  %s [%d/%d ~%d%%] %s", label, n, total, done*100/all, name)
			} else {
				o.logf("  %s [%d/%d] %s", label, n, total, name)
			}
		},
		OnTick: func(t tempdb.Tick) {
			if t.N == 0 {
				o.logf("  %s: working, no table yet (%s received, %s elapsed)", label, humanBytes(t.Bytes), runlog.Round(t.Elapsed))
				return
			}
			o.logf("  %s: still on %s (table %d/%d): %s in this table, %s received, %s elapsed",
				label, t.Table, t.N, total, runlog.Round(t.InTable), humanBytes(t.Bytes), runlog.Round(t.Elapsed))
		},
	}
}

// tableSizes reads the approximate size of each table from the source. It is only used for the progress
// percentage, so any failure just means "no percentage".
func tableSizes(ctx context.Context, d engine.Dialect, db *sql.DB, database string, tables []string) map[string]int64 {
	rows, err := db.QueryContext(ctx, d.TableSizesSQL(database, tables))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil
		}
		out[name] = size
	}
	return out
}

type countReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// watchBytes prints, every heartbeat, how many bytes a long file operation has processed (of total, when
// known). Call the returned function when the operation ends.
func (o *Options) watchBytes(label string, total int64, n *atomic.Int64) (stop func()) {
	start := time.Now()
	done, fin := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(fin)
		t := time.NewTicker(o.every())
		defer t.Stop()
		for {
			select {
			case <-t.C:
				cur := n.Load()
				if total > 0 {
					o.logf("  %s: %s of %s (%d%%), %s elapsed", label, humanBytes(cur), humanBytes(total), cur*100/total, runlog.Round(time.Since(start)))
				} else {
					o.logf("  %s: %s so far, %s elapsed", label, humanBytes(cur), runlog.Round(time.Since(start)))
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-fin }
}

// runningSet tracks which tables are being anonymised, for the heartbeat.
type runningSet struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (r *runningSet) add(t string) {
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]time.Time{}
	}
	r.m[t] = time.Now()
	r.mu.Unlock()
}

func (r *runningSet) remove(t string) { r.mu.Lock(); delete(r.m, t); r.mu.Unlock() }

// describe is "a (12s), b (3s)", longest running first.
func (r *runningSet) describe() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.m))
	for t := range r.m {
		names = append(names, t)
	}
	sort.Slice(names, func(i, j int) bool { return r.m[names[i]].Before(r.m[names[j]]) })
	parts := make([]string, len(names))
	for i, t := range names {
		parts[i] = fmt.Sprintf("%s (%s)", t, runlog.Round(time.Since(r.m[t])))
	}
	return strings.Join(parts, ", ")
}
