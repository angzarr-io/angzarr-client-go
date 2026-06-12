//go:build acceptance

package features

// Acceptance hook: the second of the two suite hooks. Where the unit hook
// (features_test.go) drives the engine in-process, this one drives the
// REAL angzarr core — the coordinator deployed in the kind cluster —
// through the real client, so the coordinator/bus-side scenarios the
// unit tier marks @wip (sequence admission, publish fan-out, correlation
// stamping, subscription matching) execute against the implementation
// that owns them.
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
	Output: colors.Colored(os.Stdout),
	Format: "pretty",
	Paths:  []string{"../angzarr-project/features/client"},
	// The unit tier's pending set: scenarios whose semantics live on the
	// coordinator/bus side. Here they are the whole point.
	Tags:        "@wip",
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
