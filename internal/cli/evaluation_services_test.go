package cli

import (
	"errors"
	"io"
	"testing"

	"github.com/atyrode/babel/internal/evaluation"
)

// A non-producing workstation must never turn missing shared-reader capability
// into a successful local-only evaluation tally. The fixture owns its entire
// catalog/configuration; it deliberately has no payload key document.
func TestEvaluationSharedReaderFailureDoesNotBecomeLocalCoverage(t *testing.T) {
	deployment := newStagingDeployment(t)
	deployment.f.seed()
	state, err := openAnalysisState()
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ledger, err := openReality()
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	a := &app{stdout: io.Discard, stderr: io.Discard}
	service, closeService, err := a.openEvaluationServices(t.Context(), state.frontier, ledger,
		state.sync, nil, nil, evaluationSourceOptions(state)...)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService()
	if err := service.Refresh(t.Context()); !errors.Is(err, evaluation.ErrUnavailable) {
		t.Fatalf("refresh without shared reader: %v", err)
	}
	page, err := service.List(t.Context(), evaluation.Query{Kind: "hypothesis", Limit: 20})
	if err != nil {
		if !errors.Is(err, evaluation.ErrUnavailable) {
			t.Fatalf("unavailable deployment returned an unrelated error: %v", err)
		}
		return
	}
	if page.Unavailable == "" {
		t.Fatalf("shared-reader failure presented a normal deployment tally: total=%d stale=%v", page.Total, page.Stale)
	}
}
