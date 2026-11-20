package tempdb

import (
	"regexp"
	"sync"
	"time"
)

// StreamOptions makes Pipe report what is flowing through it. The zero value reports nothing.
type StreamOptions struct {
	Marker *regexp.Regexp // matches the line that starts a table; group 1 = its name
	Total  int            // number of tables expected (for the "n/total" display)
	Every  time.Duration  // heartbeat interval; 0 = 10s

	// OnTable is called when a new table starts (n counts from 1).
	OnTable func(name string, n int)
	// OnTick is called every Every, even when nothing new happened, so a slow table is visible.
	OnTick func(t Tick)
}

// Tick is the state of a stream at a heartbeat.
type Tick struct {
	Table   string        // table being streamed ("" before the first one)
	N       int           // how many tables have started
	Bytes   int64         // bytes streamed so far
	InTable time.Duration // time spent on the current table
	Elapsed time.Duration // time since the stream started
}

// carryLen is how much of the previous chunk is kept so a marker split between two writes is still found.
const carryLen = 256

// meter is an io.Writer that counts the bytes written to it and finds table markers in them.
type meter struct {
	so       StreamOptions
	mu       sync.Mutex
	carry    []byte
	bytes    int64
	n        int
	cur      string
	start    time.Time
	curStart time.Time
}

func newMeter(so StreamOptions) *meter {
	now := time.Now()
	return &meter{so: so, start: now, curStart: now}
}

func (m *meter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes += int64(len(p))
	data := make([]byte, 0, len(m.carry)+len(p))
	data = append(append(data, m.carry...), p...)
	old := len(m.carry)
	if m.so.Marker != nil {
		for _, loc := range m.so.Marker.FindAllSubmatchIndex(data, -1) {
			if loc[1] <= old { // entirely inside the previous chunk: already seen
				continue
			}
			name := string(data[loc[2]:loc[3]])
			if name == m.cur {
				continue
			}
			m.n++
			m.cur, m.curStart = name, time.Now()
			if m.so.OnTable != nil {
				m.so.OnTable(name, m.n)
			}
		}
	}
	if len(data) > carryLen {
		data = data[len(data)-carryLen:]
	}
	m.carry = append(m.carry[:0], data...)
	return len(p), nil
}

func (m *meter) snapshot() Tick {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	return Tick{Table: m.cur, N: m.n, Bytes: m.bytes, InTable: now.Sub(m.curStart), Elapsed: now.Sub(m.start)}
}

// startTicker runs the heartbeat until the returned function is called.
func (m *meter) startTicker() (stop func()) {
	if m.so.OnTick == nil {
		return func() {}
	}
	every := m.so.Every
	if every <= 0 {
		every = 10 * time.Second
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.so.OnTick(m.snapshot())
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-finished }
}
