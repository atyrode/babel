package evaluation

import (
	"context"
	"testing"
	"time"
)

// countingSource answers an empty inventory and counts what it was asked for,
// which is the cost a sweep pays: one full scan of the deployment's artifacts.
type countingSource struct {
	Resolver
	scans int
}

func (c *countingSource) Artifacts(context.Context) ([]Artifact, error) {
	c.scans++
	return nil, nil
}

func (c *countingSource) EvaluationRecords(context.Context) ([]Record, error) { return nil, nil }

// A coverage sweep is shared, not repeated per process.
//
// Each `babel evaluate` is its own process, so a cadence remembered in memory
// is always zero at startup: on 2026-09-12, 32 concurrent draws over 4,692
// records each began a full scan, produced three engine sessions and recorded
// no reviews. The cadence has to live where every process can see it.
func TestCoverageSweepIsSharedByEveryProcess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src := &countingSource{Resolver: h.resolver}
	dir := t.TempDir()

	first, err := NewService(dir, h.store, src)
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	defer first.Close()
	if _, err := first.Check(ctx); err != nil {
		t.Fatalf("first check: %v", err)
	}
	if src.scans != 1 {
		t.Fatalf("the first sweep scanned %d times, want 1", src.scans)
	}

	if _, err := first.Check(ctx); err != nil {
		t.Fatalf("second check: %v", err)
	}
	if src.scans != 1 {
		t.Errorf("a sweep inside the cadence rescanned: %d scans", src.scans)
	}

	// A second service over the same projection directory is what a second
	// `babel evaluate` process is. It must read the cadence the first one
	// established rather than starting its own.
	second, err := NewService(dir, h.store, src)
	if err != nil {
		t.Fatalf("open second service: %v", err)
	}
	defer second.Close()
	if _, err := second.Check(ctx); err != nil {
		t.Fatalf("other process check: %v", err)
	}
	if src.scans != 1 {
		t.Errorf("a second process rescanned inside the cadence: %d scans", src.scans)
	}
}

// The lease admits one rebuilder at a time and expires rather than wedging.
func TestSweepLeaseAdmitsOneRebuilderAtATime(t *testing.T) {
	ctx := context.Background()
	proj, err := openProjection(t.TempDir())
	if err != nil {
		t.Fatalf("open projection: %v", err)
	}
	defer proj.Close()

	now := time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC)
	taken, err := proj.claimSweep(ctx, now, time.Minute)
	if err != nil || !taken {
		t.Fatalf("first claim = %v, %v; want true", taken, err)
	}
	again, err := proj.claimSweep(ctx, now.Add(10*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again {
		t.Error("two processes were admitted to the same rebuild")
	}
	expired, err := proj.claimSweep(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil || !expired {
		t.Fatalf("claim after expiry = %v, %v; want true", expired, err)
	}
	proj.releaseSweep(ctx)
	free, err := proj.claimSweep(ctx, now.Add(2*time.Minute+time.Second), time.Minute)
	if err != nil || !free {
		t.Fatalf("claim after release = %v, %v; want true", free, err)
	}
}
