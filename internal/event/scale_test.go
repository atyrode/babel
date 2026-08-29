package event_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/synth"
)

// TestScanSurvivesAnExtremeCorpus is the property the design exists for. Real
// harness sessions reach a third of a gigabyte in one file, and one record can
// reach the tens of megabytes, so a scanner is only correct if its memory is a
// function of the largest record rather than of the file. Generating the corpus
// here rather than committing it keeps the test hermetic at a size no fixture
// could be checked in at.
//
// The bound is on peak heap, not on cumulative allocation. Building each
// event's text legitimately allocates once per record, so total allocation
// scales with the corpus and says nothing about whether the file was buffered;
// resident heap is what distinguishes streaming from slurping.
func TestScanSurvivesAnExtremeCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("generating and scanning a corpus above 320 MiB is far too slow for -short")
	}

	root := t.TempDir()
	corpus, err := synth.Generate(root, synth.ExtremeProfile())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var target synth.Session
	for _, s := range corpus.Sessions {
		if s.Harness == synth.HarnessOMP && s.Bytes > target.Bytes {
			target = s
		}
	}
	if target.Bytes <= 320<<20 {
		t.Fatalf("largest generated OMP log is %d bytes; want above 320 MiB", target.Bytes)
	}

	file, err := os.Open(target.Path)
	if err != nil {
		t.Fatalf("open generated log: %v", err)
	}
	defer file.Close()

	stream := event.Stream{Harness: synth.HarnessOMP, SourceID: target.ID, Path: target.Path}

	runtime.GC()
	var sample runtime.MemStats
	runtime.ReadMemStats(&sample)
	baseline := sample.HeapAlloc

	var (
		events, opaque, partial int
		lastOffset              int64
		peak                    uint64
	)
	err = event.Scan(file, stream, func(e event.Event) error {
		events++
		if e.Kind == event.KindOpaque {
			opaque++
		}
		if e.Partial {
			partial++
		}
		// Locators must advance monotonically, or an evidence reference
		// recovered later would read the wrong bytes.
		if e.Locator.ByteOffset < lastOffset {
			t.Fatalf("event %d offset %d went backwards from %d",
				e.Index, e.Locator.ByteOffset, lastOffset)
		}
		lastOffset = e.Locator.ByteOffset
		if events%4096 == 0 {
			runtime.ReadMemStats(&sample)
			peak = max(peak, sample.HeapAlloc)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if events == 0 {
		t.Fatal("scanned no events")
	}
	if opaque == 0 {
		t.Errorf("scanned %d events with no opaque record; a session's non-message header records carry no analysis category", events)
	}

	// Generous next to the 16 MiB record budget the oversized record forces
	// the buffer to, and far below the third of a gigabyte a buffering
	// implementation would hold.
	const limit = 96 << 20
	if peak > baseline+limit {
		t.Errorf("peak heap %d exceeded baseline %d by more than %d while scanning %d bytes",
			peak, baseline, limit, target.Bytes)
	}
	t.Logf("scanned %d bytes in %s: %d events, %d opaque, %d partial, peak heap %d bytes above a %d baseline",
		target.Bytes, filepath.Base(target.Path), events, opaque, partial, peak-baseline, baseline)
}

// TestScanDegradesEveryPlantedDefect walks the whole generated corpus and
// checks the scanner against the defects the generator says it planted, per
// session. This is the pairing that makes the fixture trustworthy: the
// generator reports where it put the damage, so the test asserts degradation
// at a known location instead of hoping a defect happened to be in the file it
// picked. SPEC 6.3 requires explicit degradation, so a defect that produced no
// partial event would be silent data loss.
func TestScanDegradesEveryPlantedDefect(t *testing.T) {
	root := t.TempDir()
	corpus, err := synth.Generate(root, synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var checked int
	for _, s := range corpus.Sessions {
		damaged := s.Defects.OversizedRecords + s.Defects.MalformedRecords
		if s.Defects.TornFinalLine {
			damaged++
		}
		if damaged == 0 {
			continue
		}
		checked++
		t.Run(s.Harness+"/"+s.ID, func(t *testing.T) {
			file, err := os.Open(s.Path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer file.Close()

			var partial, events int
			stream := event.Stream{Harness: s.Harness, SourceID: s.ID, Path: s.Path}
			if err := event.Scan(file, stream, func(e event.Event) error {
				events++
				if e.Partial {
					partial++
					if e.Kind != event.KindOpaque {
						t.Errorf("partial event %d has kind %q; damaged evidence carries no analysis category",
							e.Index, e.Kind)
					}
					if e.Locator.Digest == "" {
						t.Errorf("partial event %d has no digest; degraded evidence must still be recoverable", e.Index)
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("scan: %v", err)
			}

			// Every planted defect must surface. More partial events than
			// planted defects would mean the scanner is degrading records the
			// generator wrote as valid.
			if partial != damaged {
				t.Errorf("scanned %d partial of %d events; generator planted %d defects (%d oversized, %d malformed, torn=%v)",
					partial, events, damaged, s.Defects.OversizedRecords,
					s.Defects.MalformedRecords, s.Defects.TornFinalLine)
			}
		})
	}
	if checked == 0 {
		t.Fatal("the default profile planted no defects; the fixture cannot prove degradation")
	}
	t.Logf("checked %d damaged sessions of %d", checked, len(corpus.Sessions))
}
