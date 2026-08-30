package index_test

import (
	"context"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/synth"
)

// TestIndexScalesWithBytesNotFiles is the property the streaming design
// exists for. The measured corpus puts a quarter of all bytes in the top one
// percent of files, so a session of tens or hundreds of megabytes is the
// normal case rather than the exception, and an implementation that
// materialized one would work on every fixture and fail on the corpus.
// Generating the tree here rather than committing it keeps the test hermetic
// at a size no fixture could be checked in at.
//
// The bound is on peak resident heap, not on total allocation: building each
// event's text and each batch's arguments allocates legitimately, and
// cumulative allocation therefore scales with the corpus while saying
// nothing about whether a file was buffered.
func TestIndexScalesWithBytesNotFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("generating and indexing a corpus with a primary log above 64 MiB is too slow for -short")
	}

	root := t.TempDir()
	corpus, err := synth.Generate(filepath.Join(root, "corpus"), synth.LargeProfile())
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}
	var largest int64
	for _, s := range corpus.Sessions {
		largest = max(largest, s.Bytes)
	}
	if largest <= 64<<20 {
		t.Fatalf("largest generated log is %d bytes; want above 64 MiB", largest)
	}

	idx, err := index.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	runtime.GC()
	var sample runtime.MemStats
	runtime.ReadMemStats(&sample)
	baseline := sample.HeapAlloc

	// Peak heap is sampled from a second goroutine, because the indexing
	// call is where the peak occurs and it has no callback to sample from.
	//
	// A sample counts only when a collection has happened since the last
	// one, because HeapAlloc includes garbage that has not been collected
	// yet: at this allocation rate the uncollected garbage of a streaming
	// implementation exceeds the live heap of a buffering one, so raw
	// samples would measure the collector rather than the design. Reading
	// the heap just after a collection measures what is live; forcing a
	// collection per sample would measure it more precisely still, and cost
	// more wall time than the indexing being measured.
	var peak atomic.Uint64
	done := make(chan struct{})
	go func() {
		var stats runtime.MemStats
		collections := uint32(0)
		for {
			select {
			case <-done:
				return
			case <-time.After(200 * time.Millisecond):
				runtime.ReadMemStats(&stats)
				if stats.NumGC == collections {
					continue
				}
				collections = stats.NumGC
				if stats.HeapAlloc > peak.Load() {
					peak.Store(stats.HeapAlloc)
				}
			}
		}
	}()

	var (
		indexedBytes int64
		records      int
		events       int
	)
	start := time.Now()
	for _, s := range corpus.Sessions {
		res, err := idx.IndexSession(context.Background(), event.Stream{
			Harness:       s.Harness,
			AdapterSchema: 1,
			SourceID:      s.ID,
			Path:          s.Path,
		})
		if err != nil {
			t.Fatalf("index %s: %v", s.Path, err)
		}
		indexedBytes += res.Bytes
		records += res.Records
		events += res.Events
	}
	elapsed := time.Since(start)
	close(done)

	if records == 0 || events == 0 {
		t.Fatalf("indexed %d records into %d events", records, events)
	}
	stats, err := idx.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Events != events {
		t.Errorf("index holds %d events, want the %d indexed", stats.Events, events)
	}

	// What dominates the live heap is not this package: an oversized record
	// makes the scanner's reusable record buffer grow to its 16 MiB budget,
	// and the buffer keeps that capacity for the rest of the session. The
	// insert batch adds a couple of megabytes on top. The bound is set below
	// the corpus's largest single log, so an implementation that materialized
	// a session — the failure this test exists to catch — still fails it.
	const limit = 64 << 20
	if peak.Load() > baseline+limit {
		t.Errorf("peak live heap grew %d bytes above a %d baseline, above the %d limit",
			peak.Load()-baseline, baseline, limit)
	}

	// The index's size relative to the bytes it indexed is an operational
	// fact: this cache lives in the operator's private state directory
	// beside a corpus measured in gigabytes. This corpus is the worst case
	// for the ratio rather than a typical one — its records are almost
	// entirely body text, where real records spend a large share of their
	// bytes on structure the index does not store — so the bound is a
	// regression guard, not a target.
	ratio := float64(stats.Bytes) / float64(indexedBytes)
	if ratio > 1.1 {
		t.Errorf("index is %.2fx the bytes it indexed; a retrieval cache that large is a design failure", ratio)
	}
	t.Logf("indexed %d bytes across %d sessions in %s (%.1f MiB/s): %d records, %d events, index %d bytes (%.3fx corpus), peak heap %d bytes above a %d baseline",
		indexedBytes, stats.Sessions, elapsed.Round(time.Millisecond),
		float64(indexedBytes)/(1<<20)/elapsed.Seconds(),
		records, events, stats.Bytes, ratio,
		peak.Load()-baseline, baseline)
}
