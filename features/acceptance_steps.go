//go:build acceptance

package features

// Step bindings for the acceptance hook (coordinator-contract tier).
// Nothing is registered yet — the tier's scenarios report undefined,
// which is the honest state until bindings drive the deployed
// coordinator for real. Do NOT wire the legacy fact_flow/merge_strategy
// step files here: they are hand-rolled simulations (substring matching,
// fake state) and simulating the coordinator is exactly what this tier
// exists to replace.

import (
	"github.com/cucumber/godog"
)

// InitAcceptanceSteps registers the coordinator-contract step bindings.
func InitAcceptanceSteps(_ *godog.ScenarioContext) {}
