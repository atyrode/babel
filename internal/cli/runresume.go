package cli

import (
	"fmt"

	"github.com/atyrode/babel/internal/explore"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// recordedExplorePlan is shared by operator and conductor recovery. Scope,
// profile and recipe versions come from the receipt, never a fresh preparation
// or current scheduling defaults. Only the executable is current configuration.
func recordedExplorePlan(receipt runstore.Receipt, wcfg worker.Config) (explorePlan, error) {
	cp := receipt.Body.Checkpoint
	if cp == nil || cp.State != runstore.Interrupted {
		return explorePlan{}, fmt.Errorf("run is not interrupted; reconcile an orphan before resuming")
	}
	if cp.Launch == nil {
		return explorePlan{}, fmt.Errorf("historical run has no trusted launch checkpoint; use babel explore --run-id with explicitly supplied preparation, recipes and profile")
	}
	launch := cp.Launch
	if len(launch.Recipes) == 0 || launch.Profile.ID == "" || launch.Profile.Revision < 1 {
		return explorePlan{}, fmt.Errorf("recorded launch inputs are incomplete")
	}
	set, err := recipeSet(launch.Recipes)
	if err != nil {
		return explorePlan{}, err
	}
	for _, recipe := range set.All() {
		found := false
		for _, asset := range receipt.Body.Cookbook {
			if asset.Ref.ID == recipe.ID && asset.Ref.Version == recipe.Version {
				found = true
				break
			}
		}
		if !found {
			return explorePlan{}, fmt.Errorf("recorded cookbook version is unavailable; refusing to substitute resume inputs")
		}
	}
	return explorePlan{
		prep: receipt.Preparation, profile: launch.Profile, recipes: set, worker: wcfg,
		prior: launch.Prior, params: launch.Params, runID: receipt.Header.RunID,
		authority: receipt.Header.Authority, roots: launch.Roots, scanRoots: launch.ScanRoots,
		research: launch.Research, challenge: launch.Challenge, synthesize: launch.Synthesize,
		budget: explore.Budget{Develop: launch.Develop, Retrievals: launch.Retrievals, Fetches: launch.Fetches},
	}, nil
}
