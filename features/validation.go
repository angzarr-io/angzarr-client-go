package features

// validation.go — step bindings for client/validation.feature (C-0070..C-0077).
//
// In Go the component declaration surface IS the proto service carrying
// (angzarr.v1.component) options, so the missing-field scenarios
// (C-0070..C-0075) drive the real protoc-gen-angzarr core
// (internal/codegen) against in-memory descriptor sets and assert that
// generation fails. The decorator-stacking scenarios use the surface where
// each conflict is expressible: C-0076 (one component declared as two
// kinds) through the engine RouterBuilder's mixed-kind rejection, and
// C-0077 (one method as both command handler and event applier) through
// descriptor resolution, which rejects the duplicate method declaration.
//
// Field mapping notes: a process manager's pm_domain and command-target
// domain are one declaration in Go — ComponentOptions.output_domain feeds
// the engine's pmDomain — so C-0072 and C-0074 both pin its omission.

import (
	"fmt"

	"github.com/cucumber/godog"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/angzarr-io/angzarr-cli/codegen"
	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
)

const (
	declTestProtoFile = "validation_test.proto"
	declTestState     = "validation.test.State"
	declTestCmd       = ".validation.test.Cmd"
	declTestEvt       = ".validation.test.Evt"
)

// handlerRPC declares a plain command/event handler rpc.
func handlerRPC(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(name),
		InputType:  proto.String(declTestCmd),
		OutputType: proto.String(declTestEvt),
	}
}

// applierRPC declares an rpc carrying (angzarr.v1.applies) = true.
func applierRPC(name string) *descriptorpb.MethodDescriptorProto {
	m := handlerRPC(name)
	m.Options = &descriptorpb.MethodOptions{}
	proto.SetExtension(m.Options, pb.E_Applies, true)
	return m
}

// reactsRPC declares a PM handler rpc carrying (angzarr.v1.reacts).domain.
func reactsRPC(name, domain string) *descriptorpb.MethodDescriptorProto {
	m := handlerRPC(name)
	m.Options = &descriptorpb.MethodOptions{}
	proto.SetExtension(m.Options, pb.E_Reacts, &pb.ReactsOptions{Domain: domain})
	return m
}

// runDeclaration compiles a one-service descriptor set and runs the real
// protoc-gen-angzarr core over it. Both descriptor resolution failures and
// generation failures are declaration-time configuration errors.
func runDeclaration(serviceName string, component *pb.ComponentOptions, methods ...*descriptorpb.MethodDescriptorProto) error {
	optionsFDP := protodesc.ToFileDescriptorProto(pb.File_angzarr_client_proto_angzarr_v1_options_proto)
	descriptorFDP := protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto)

	svcOpts := &descriptorpb.ServiceOptions{}
	proto.SetExtension(svcOpts, pb.E_Component, component)

	file := &descriptorpb.FileDescriptorProto{
		Name:       proto.String(declTestProtoFile),
		Package:    proto.String("validation.test"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{optionsFDP.GetName()},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.test/validation;validationtest"),
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("State")},
			{Name: proto.String("Cmd")},
			{Name: proto.String("Evt")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:    proto.String(serviceName),
			Options: svcOpts,
			Method:  methods,
		}},
	}

	gen, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{declTestProtoFile},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{descriptorFDP, optionsFDP, file},
	})
	if err != nil {
		return err
	}
	return codegen.Generate(gen, "go")
}

// ValidationContext holds per-scenario state for validation.feature.
type ValidationContext struct {
	declErr error
	// C-0076: the component declared under two kinds.
	className string
	aggregate angzarr.CommandDispatcher
	// C-0077: the method declared as both handler and applier.
	methodName string
}

func newValidationContext() *ValidationContext {
	return &ValidationContext{}
}

// InitValidationSteps registers validation.feature step definitions.
func InitValidationSteps(ctx *godog.ScenarioContext) {
	vc := newValidationContext()

	// --- When: declarations missing required fields (C-0070..C-0075) ---
	ctx.Step(`^I declare an aggregate handler for domain "([^"]*)" without state$`, vc.whenDeclareAggregateWithoutState)
	ctx.Step(`^I declare a saga named "([^"]*)" from "([^"]*)" without target$`, vc.whenDeclareSagaWithoutTarget)
	ctx.Step(`^I declare a process manager for name "([^"]*)" without pm_domain$`, vc.whenDeclareProcessManagerWithoutPMDomain)
	ctx.Step(`^I declare a process manager for name "([^"]*)" without sources$`, vc.whenDeclareProcessManagerWithoutSources)
	ctx.Step(`^I declare a process manager for name "([^"]*)" without targets$`, vc.whenDeclareProcessManagerWithoutTargets)
	ctx.Step(`^I declare a projector named "([^"]*)" without domains$`, vc.whenDeclareProjectorWithoutDomains)

	// --- Given/When: kind- and method-stacking conflicts (C-0076/C-0077) ---
	ctx.Step(`^a class Order$`, vc.givenClassOrder)
	ctx.Step(`^Order is declared as an aggregate handler$`, vc.givenOrderDeclaredAsAggregate)
	ctx.Step(`^I also declare Order as a saga$`, vc.whenAlsoDeclareOrderAsSaga)
	ctx.Step(`^a method "([^"]*)" declared as a command handler$`, vc.givenMethodDeclaredAsCommandHandler)
	ctx.Step(`^I also declare "([^"]*)" as an event applier$`, vc.whenAlsoDeclareMethodAsApplier)

	// --- Then ---
	ctx.Step(`^the declaration raises a configuration error$`, vc.thenDeclarationRaisesConfigError)
}

// C-0070: aggregate requires state.
func (v *ValidationContext) whenDeclareAggregateWithoutState(domain string) error {
	v.declErr = runDeclaration("OrderAggregate", &pb.ComponentOptions{
		Kind:        pb.ComponentKind_COMPONENT_KIND_AGGREGATE,
		InputDomain: domain,
	}, handlerRPC("HandleCmd"))
	return nil
}

// C-0071: saga requires a target (output_domain).
func (v *ValidationContext) whenDeclareSagaWithoutTarget(name, from string) error {
	v.declErr = runDeclaration(name, &pb.ComponentOptions{
		Kind:        pb.ComponentKind_COMPONENT_KIND_SAGA,
		InputDomain: from,
	}, handlerRPC("HandleEvt"))
	return nil
}

// C-0072: pm_domain and targets are one declaration in Go (output_domain).
func (v *ValidationContext) whenDeclareProcessManagerWithoutPMDomain(name string) error {
	v.declErr = runDeclaration(name, &pb.ComponentOptions{
		Kind:  pb.ComponentKind_COMPONENT_KIND_PROCESS_MANAGER,
		State: declTestState,
	}, reactsRPC("HandleEvt", "orders"))
	return nil
}

// C-0073: PM handler rpcs require (angzarr.v1.reacts).domain — the sources.
func (v *ValidationContext) whenDeclareProcessManagerWithoutSources(name string) error {
	v.declErr = runDeclaration(name, &pb.ComponentOptions{
		Kind:         pb.ComponentKind_COMPONENT_KIND_PROCESS_MANAGER,
		OutputDomain: "fulfillment",
		State:        declTestState,
	}, handlerRPC("HandleEvt"))
	return nil
}

// C-0074: see C-0072 — omitting output_domain drops both pm_domain and targets.
func (v *ValidationContext) whenDeclareProcessManagerWithoutTargets(name string) error {
	return v.whenDeclareProcessManagerWithoutPMDomain(name)
}

// C-0075: projector requires its subscribed domains (input_domain).
func (v *ValidationContext) whenDeclareProjectorWithoutDomains(name string) error {
	v.declErr = runDeclaration(name, &pb.ComponentOptions{
		Kind:  pb.ComponentKind_COMPONENT_KIND_PROJECTOR,
		State: declTestState,
	}, handlerRPC("HandleEvt"))
	return nil
}

// C-0076: one component declared as two kinds. In Go the conflict surfaces
// when the declarations meet the RouterBuilder: a router hosts exactly one
// handler kind, so registering Order-as-aggregate and Order-as-saga fails
// the build before any dispatch.
func (v *ValidationContext) givenClassOrder() error {
	v.className = "Order"
	return nil
}

func (v *ValidationContext) givenOrderDeclaredAsAggregate() error {
	v.aggregate = simpleCH(v.className, "order", fqCreateOrder)
	return nil
}

func (v *ValidationContext) whenAlsoDeclareOrderAsSaga() error {
	saga := angzarr.NewSagaDispatch(v.className, "order", "inventory")
	_, v.declErr = angzarr.NewRouterBuilder().
		Register(v.aggregate).
		Register(saga).
		Build()
	return nil
}

// C-0077: one method declared as both command handler and event applier.
// The declaration surface rejects it — a service cannot declare the same
// rpc name twice, so descriptor resolution fails before generation.
func (v *ValidationContext) givenMethodDeclaredAsCommandHandler(method string) error {
	v.methodName = method
	return nil
}

func (v *ValidationContext) whenAlsoDeclareMethodAsApplier(method string) error {
	v.declErr = runDeclaration("OrderAggregate", &pb.ComponentOptions{
		Kind:        pb.ComponentKind_COMPONENT_KIND_AGGREGATE,
		InputDomain: "order",
		State:       declTestState,
	}, handlerRPC(v.methodName), applierRPC(method))
	return nil
}

func (v *ValidationContext) thenDeclarationRaisesConfigError() error {
	if v.declErr == nil {
		return fmt.Errorf("expected a configuration error at declaration/build time, got none")
	}
	return nil
}
