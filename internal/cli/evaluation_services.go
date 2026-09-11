package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// openEvaluationServices borrows the analysis and fleet readers. Its cleanup
// releases only its projection, durable evaluation handle and coordination pool.
// Configuration selects shared coordination even when the catalog is down:
// failing a shared claim must never silently spend a local copy of the budget.
func (a *app) openEvaluationServices(ctx context.Context, front *frontier.Store, ledger *reality.Store,
	hook babelsync.Hook, remote *fleet.Reader, coord evaluation.Coordinator, sourceOptions ...evaluation.SourceOption) (*evaluation.Service, func(), error) {
	d, err := babelDirs()
	if err != nil {
		return nil, func() {}, err
	}
	cfg, found, err := config.Load()
	if err != nil {
		return nil, func() {}, err
	}
	var sourceRemote evaluation.FleetSource
	if remote != nil {
		sourceRemote = remote
	}
	source := evaluation.NewSource(front, ledger, sourceRemote, sourceOptions...)
	shared := stagingUnavailable(cfg, found) == ""
	if shared && remote == nil {
		source = unavailableEvaluationSource{Source: source}
	}
	var ownedCoord *evaluationCatalogCoordinator
	if coord == nil && shared {
		ownedCoord = &evaluationCatalogCoordinator{cfg: cfg}
		coord = ownedCoord
	}
	opts := []evaluation.Option{evaluation.WithSync(hook)}
	if coord != nil {
		opts = append(opts, evaluation.WithCoordinator(coord))
	}
	if resolver, ok := source.(evaluation.RecordResolver); ok {
		opts = append(opts, evaluation.WithRecordResolver(resolver))
	}
	store, err := evaluation.Open(d.durableDir(), source, opts...)
	if err != nil {
		if ownedCoord != nil {
			ownedCoord.Close()
		}
		return nil, func() {}, err
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, 30*time.Second)
	_, recoveryErr := store.Recover(recoveryCtx)
	cancelRecovery()
	if recoveryErr != nil {
		a.diagf("warning: recover evaluation completions: %s\n", Sanitize(recoveryErr.Error()))
	}
	service, err := evaluation.NewService(d.indexDir(), store, source)
	if err != nil {
		store.Close()
		if ownedCoord != nil {
			ownedCoord.Close()
		}
		return nil, func() {}, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if err := service.Close(); err != nil {
				a.diagf("warning: close evaluation projection: %s\n", Sanitize(err.Error()))
			}
			if err := store.Close(); err != nil {
				a.diagf("warning: close evaluation state: %s\n", Sanitize(err.Error()))
			}
			if ownedCoord != nil {
				if err := ownedCoord.Close(); err != nil {
					a.diagf("warning: close evaluation coordination: %s\n", Sanitize(err.Error()))
				}
			}
		})
	}
	return service, cleanup, nil
}

func evaluationSourceOptions(state *analysisState) []evaluation.SourceOption {
	options := []evaluation.SourceOption{evaluation.WithComplaints(state.complaints)}
	if state.runs != nil {
		options = append(options, evaluation.WithProducedCounter("receipt", func(ctx context.Context) (int, error) {
			_, total, err := state.runs.Receipts(ctx, 1, 0)
			return total, err
		}))
	}
	return options
}

// A missing shared reader is an unavailable deployment view, not local mode.
// Existing projection data remains readable with its stale/unavailable marker.
type unavailableEvaluationSource struct{ evaluation.Source }

func (s unavailableEvaluationSource) Artifacts(context.Context) ([]evaluation.Artifact, error) {
	return nil, fmt.Errorf("%w: the shared artifact reader is unavailable", evaluation.ErrUnavailable)
}

func (s unavailableEvaluationSource) EvaluationRecords(context.Context) ([]evaluation.Record, error) {
	return nil, fmt.Errorf("%w: the shared evaluation reader is unavailable", evaluation.ErrUnavailable)
}

// Refresh is process-owned, not request-owned. A page reads the existing
// projection immediately; closing the web session cancels and joins its reader
// before any borrowed database handles are released. No inference runs here.
func (a *app) refreshEvaluations(service *evaluation.Service) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			refreshCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
			err := service.Refresh(refreshCtx)
			stop()
			if err != nil && ctx.Err() == nil {
				a.diagf("warning: evaluation view refresh: %s\n", Sanitize(err.Error()))
			}
			if ctx.Err() != nil {
				return
			}
			interval := time.Minute
			if policy, err := service.Policy(ctx); err == nil && policy.CadenceSeconds > 0 {
				interval = time.Duration(policy.CadenceSeconds) * time.Second
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Credentials stay in the trusted process, never in assignment documents or
// tool arguments. Opening the pool is deferred until coordination is needed.
type evaluationCatalogCoordinator struct {
	mu     sync.Mutex
	cfg    config.Config
	db     *sql.DB
	closed bool
}

func (c *evaluationCatalogCoordinator) connection(ctx context.Context) (*sql.DB, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("%w: evaluation coordination is closed", evaluation.ErrUnavailable)
	}
	if c.db != nil {
		return c.db, nil
	}
	if c.cfg.Catalog == nil {
		return nil, fmt.Errorf("%w: shared storage names no catalog", evaluation.ErrUnavailable)
	}
	db, err := sharedcatalog.Open(ctx, c.cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(c.cfg.Catalog.MaxConnections))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", evaluation.ErrUnavailable, err)
	}
	c.db = db
	return db, nil
}

func (c *evaluationCatalogCoordinator) Claim(ctx context.Context, assignment evaluation.Assignment, policy evaluation.Policy) (evaluation.Assignment, error) {
	db, err := c.connection(ctx)
	if err != nil {
		return evaluation.Assignment{}, err
	}
	claim, err := sharedcatalog.ClaimEvaluation(ctx, db, sharedcatalog.EvaluationClaim{
		DeploymentID:  c.cfg.DeploymentID,
		ID:            assignment.ID,
		SubjectID:     assignment.Subject.ID,
		SubjectKind:   assignment.Subject.Kind,
		RunID:         assignment.RunID,
		OwnerID:       c.cfg.InstanceID,
		PolicyVersion: assignment.PolicyVersion,
		ReservedCost:  assignment.ReservedCost,
	}, sharedcatalog.EvaluationBudget{
		DailyCost:    policy.DailyCost,
		PerCycleCost: policy.PerCycleCost,
		LeaseSeconds: policy.LeaseSeconds,
	})
	if err != nil {
		return evaluation.Assignment{}, evaluationCoordinationError(err)
	}
	assignment.Fence, assignment.ExpiresAt = claim.Fence, claim.ExpiresAt
	assignment.ReservedCost = claim.ReservedCost
	return assignment, nil
}

func (c *evaluationCatalogCoordinator) Validate(ctx context.Context, id, runID string, fence int64) error {
	db, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return evaluationCoordinationError(sharedcatalog.ValidateEvaluationClaim(ctx, db, c.cfg.DeploymentID, id, runID, fence))
}

func (c *evaluationCatalogCoordinator) Finish(ctx context.Context, id, runID string, fence int64, cost float64) error {
	db, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return evaluationCoordinationError(sharedcatalog.FinishEvaluationClaim(ctx, db, c.cfg.DeploymentID, id, runID, fence, cost))
}

func evaluationCoordinationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sharedcatalog.ErrEvaluationOverrun):
		return fmt.Errorf("%w: %w", evaluation.ErrOverrun, err)
	case errors.Is(err, sharedcatalog.ErrEvaluationBudget):
		return fmt.Errorf("%w: %w", evaluation.ErrBudget, err)
	case errors.Is(err, sharedcatalog.ErrEvaluationConflict):
		return fmt.Errorf("%w: %w", evaluation.ErrConflict, err)
	case errors.Is(err, sharedcatalog.ErrEvaluationInvalid):
		return fmt.Errorf("%w: %w", evaluation.ErrInvalid, err)
	case errors.Is(err, sharedcatalog.ErrEvaluationNotFound):
		return fmt.Errorf("%w: %w", evaluation.ErrNotFound, err)
	default:
		return fmt.Errorf("%w: %w", evaluation.ErrUnavailable, err)
	}
}

func (c *evaluationCatalogCoordinator) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}
