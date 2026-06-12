package features

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"
)

var opts = godog.Options{
	Output:      colors.Colored(os.Stdout),
	Format:      "progress",
	Paths:       []string{"../angzarr-project/features/client"},
	Randomize:   0,
	Concurrency: 1,
	Strict:      false, // Allow pending scenarios without failing
}

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options:             &opts,
	}

	if suite.Run() != 0 {
		t.Fail()
	}
}

func InitializeScenario(ctx *godog.ScenarioContext) {
	// Command builder steps
	InitCommandBuilderSteps(ctx)

	// Query builder steps
	InitQueryBuilderSteps(ctx)

	// Router steps
	InitRouterSteps(ctx)

	// Event decoding steps (before state building - order matters for shared step patterns)
	InitEventDecodingSteps(ctx)

	// State building steps
	InitStateBuildingSteps(ctx)

	// Error handling steps
	InitErrorHandlingSteps(ctx)

	// Compensation steps
	InitCompensationSteps(ctx)

	// Aggregate client steps (router scenarios)
	InitAggregateSteps(ctx)

	// Connection steps
	InitConnectionSteps(ctx)

	// Query client steps
	InitQueryClientSteps(ctx)

	// Speculative client steps
	InitSpeculativeClientSteps(ctx)

	// Domain client steps
	InitDomainClientSteps(ctx)

	// Fact flow steps
	InitFactFlowSteps(ctx)

	// Merge strategy steps
	InitMergeStrategySteps(ctx)

	// Validation steps (client/validation.feature, C-0070..C-0077)
	InitValidationSteps(ctx)

	// Upcaster steps (client/upcaster.feature, C-0123..C-0125, C-0136..C-0137)
	InitUpcasterSteps(ctx)

	// Edition propagation steps (coordinator-contract/edition_propagation.feature, C-0138..C-0145).
	// Also owns the shared single-target saga "translating from ... to ..."
	// matcher used by saga.feature and command_handler/builder backgrounds.
	InitEditionPropagationSteps(ctx)

	// --- Batch 6+7 client features (WIP step harnesses) ---

	// Builder steps (client/builder.feature, C-0060..C-0065, C-0088)
	InitBuilderSteps(ctx)

	// Command-handler dispatch steps
	// (client/command_handler.feature, C-0001..C-0006, C-0085, C-0146..C-0148)
	InitCommandHandlerSteps(ctx)

	// Multi-handler dispatch steps
	// (client/multi_handler.feature, C-0010..C-0015, C-0087)
	InitMultiHandlerSteps(ctx)

	// Process-manager dispatch steps
	// (client/process_manager.feature, C-0020..C-0022)
	InitProcessManagerSteps(ctx)

	// Projector dispatch steps
	// (client/projector.feature, C-0030..C-0032, C-0086)
	InitProjectorSteps(ctx)

	// Rejection compensation steps
	// (client/rejection.feature, C-0040..C-0042)
	InitRejectionSteps(ctx)

	// Rejected-compensation detail steps
	// (client/rejected_compensation.feature, C-0080..C-0084)
	InitRejectedCompensationSteps(ctx)

	// Saga dispatch steps (client/saga.feature, C-0050..C-0053)
	InitSagaSteps(ctx)
}
