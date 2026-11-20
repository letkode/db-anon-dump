package tempdb

import (
	"regexp"
	"sync"
	"testing"
	"time"
)

var testMarker = regexp.MustCompile("(?m)^(?:CREATE TABLE|LOCK TABLES) `([^`]+)`")

func TestMeterFindsTablesAcrossChunks(t *testing.T) {
	var names []string
	m := newMeter(StreamOptions{Marker: testMarker, OnTable: func(n string, _ int) { names = append(names, n) }})
	dump := "-- header\nDROP TABLE IF EXISTS `a`;\nCREATE TABLE `a` (\n id int\n);\nINSERT INTO `a` VALUES (1,'CREATE TABLE `fake`');\n" +
		"LOCK TABLES `b` WRITE;\nCREATE TABLE `user_info_personal` (\n x int\n);\n"
	// feed it in awkward chunk sizes, so markers get split between writes
	for _, size := range []int{1, 3, 7, 1000} {
		names = nil
		m = newMeter(StreamOptions{Marker: testMarker, OnTable: func(n string, _ int) { names = append(names, n) }})
		for i := 0; i < len(dump); i += size {
			end := min(i+size, len(dump))
			if _, err := m.Write([]byte(dump[i:end])); err != nil {
				t.Fatal(err)
			}
		}
		if got := len(names); got != 3 || names[0] != "a" || names[1] != "b" || names[2] != "user_info_personal" {
			t.Errorf("chunk size %d: tables = %v", size, names)
		}
		if m.bytes != int64(len(dump)) {
			t.Errorf("chunk size %d: counted %d bytes, want %d", size, m.bytes, len(dump))
		}
	}
}

func TestMeterTicksWhileASingleTableIsSlow(t *testing.T) {
	var mu sync.Mutex
	var ticks []Tick
	m := newMeter(StreamOptions{Marker: testMarker, Every: 20 * time.Millisecond,
		OnTick: func(tk Tick) { mu.Lock(); ticks = append(ticks, tk); mu.Unlock() }})
	stop := m.startTicker()
	m.Write([]byte("CREATE TABLE `slow` (\n"))
	time.Sleep(110 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(ticks) < 2 {
		t.Fatalf("expected several heartbeats, got %d", len(ticks))
	}
	last := ticks[len(ticks)-1]
	if last.Table != "slow" || last.N != 1 || last.Bytes == 0 || last.InTable <= 0 {
		t.Errorf("bad tick: %+v", last)
	}
}

func TestMeterWithoutMarkerOnlyCounts(t *testing.T) {
	m := newMeter(StreamOptions{})
	m.Write([]byte("abc"))
	if s := m.snapshot(); s.Bytes != 3 || s.N != 0 {
		t.Errorf("%+v", s)
	}
	m.startTicker()() // no OnTick: must be a no-op
}
