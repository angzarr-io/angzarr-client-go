//go:build acceptance

package features

// Acceptance hook: the second of the two suite hooks. Where the unit hook
// (features_test.go) drives the engine in-process, this one runs against
// the REAL angzarr core deployed in kind, dialed through the real client.
//
// Its tier is features/coordinator-contract/ — the living documentation
// of coordinator-side behavior (fact flow, merge strategies, state
// building, edition propagation) that no suite executes yet. Poker-shaped
// functional coverage lives in the example tiers (examples-* repos); this
// hook exists for the generic-vocabulary coordinator contract once step
// definitions drive the deployed coordinator for real.
//
// Connectivity comes from ANGZARR_ACCEPTANCE_ENDPOINT (e.g. a
// `kubectl port-forward` of the coordinator/gateway service); the suite
// refuses to run without it rather than silently testing nothing.

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"
)

// EnvAcceptanceEndpoint names the coordinator endpoint the acceptance
// suite dials (host:port of a port-forwarded kind service).
const EnvAcceptanceEndpoint = "ANGZARR_ACCEPTANCE_ENDPOINT"

var acceptanceOpts = godog.Options{
	Output:      colors.Colored(os.Stdout),
	Format:      "pretty",
	Paths:       []string{"../angzarr-project/features/coordinator-contract"},
	Randomize:   0,
	Concurrency: 1,
	Strict:      false,
}

func TestAcceptanceFeatures(t *testing.T) {
	if os.Getenv(EnvAcceptanceEndpoint) == "" {
		t.Skipf("%s not set — start a port-forward to the kind cluster's coordinator and export it", EnvAcceptanceEndpoint)
	}
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeAcceptanceScenario,
		Options:             &acceptanceOpts,
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}

func InitializeAcceptanceScenario(ctx *godog.ScenarioContext) {
	InitAcceptanceSteps(ctx)
}
