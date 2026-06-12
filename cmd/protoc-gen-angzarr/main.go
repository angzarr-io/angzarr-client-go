// protoc-gen-angzarr is the buf/protoc plugin binary; the generation core
// lives in internal/codegen so the godog validation harness and unit tests
// can drive it in-process.
package main

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/benjaminabbitt/angzarr/client/go/internal/codegen"
)

func main() {
	protogen.Options{}.Run(codegen.Run)
}
