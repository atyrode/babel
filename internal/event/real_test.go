package event

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
)

// realRootsEnv opts the real-tree smoke test in, matching the switch every
// adapter package uses. It is skipped by default: the hermetic fixtures are
// the contract, and this only checks that the classification rules still fire
// against whatever a live machine happens to hold.
const realRootsEnv = "BABEL_SMOKE_REAL_ROOTS"

// TestScanRealRoots scans real local session logs and reports only counts.
// It never logs transcript text, paths, or session identities: this test runs
// on operator machines and its output must stay publishable.
func TestScanRealRoots(t *testing.T) {
	if os.Getenv(realRootsEnv) == "" {
		t.Skipf("set %s to scan the real harness roots", realRootsEnv)
	}
	adapters := map[string]adapter.Adapter{
		HarnessOMP:    omp.New(),
		HarnessCodex:  codex.New(),
		HarnessClaude: claude.New(),
	}
	evidence := []Kind{KindUserReport, KindAgentClaim, KindToolObservation, KindRepositoryChange, KindVerificationEvidence}
	for harness, a := range adapters {
		t.Run(harness, func(t *testing.T) {
			roots := a.DefaultRoots()
			if len(roots) == 0 {
				t.Skip("no home directory available")
			}
			if _, err := os.Stat(roots[0]); err != nil {
				t.Skipf("default %s root is absent", harness)
			}
			sessions, err := a.Discover(context.Background(), roots)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			kinds := make(map[Kind]int)
			partial, scanned := 0, 0
			for _, s := range sessions {
				if scanned >= 50 && haveAll(kinds, evidence) {
					break
				}
				f, err := os.Open(s.PrimaryPath)
				if err != nil {
					continue
				}
				stream := Stream{Harness: harness, AdapterSchema: a.Schema(), SourceID: s.SourceID, Path: s.PrimaryPath}
				err = Scan(f, stream, func(e Event) error {
					kinds[e.Kind]++
					if e.Partial {
						partial++
					}
					return nil
				})
				f.Close()
				if err != nil {
					t.Errorf("Scan failed on a real %s session: %v", harness, err)
				}
				scanned++
			}
			if scanned == 0 {
				t.Skipf("no readable %s sessions", harness)
			}
			total := 0
			for _, n := range kinds {
				total += n
			}
			t.Logf("scanned %d of %d sessions: %d events, %d partial", scanned, len(sessions), total, partial)
			for _, kind := range sortedKinds(kinds) {
				t.Logf("  %-24s %d (%.1f%%)", kind, kinds[kind], 100*float64(kinds[kind])/float64(total))
			}
			for _, kind := range evidence {
				if kinds[kind] == 0 {
					t.Errorf("kind %q never produced from real %s sessions", kind, harness)
				}
			}
		})
	}
}

func haveAll(kinds map[Kind]int, want []Kind) bool {
	for _, kind := range want {
		if kinds[kind] == 0 {
			return false
		}
	}
	return true
}

func sortedKinds(kinds map[Kind]int) []Kind {
	out := make([]Kind, 0, len(kinds))
	for kind := range kinds {
		out = append(out, kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
