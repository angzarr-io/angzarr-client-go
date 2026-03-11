# angzarr-client-go

Go client library for Angzarr event sourcing framework.

## Installation

```bash
go get github.com/angzarr-io/angzarr-client-go
```

## Usage

```go
import (
    angzarr "github.com/angzarr-io/angzarr-client-go"
    "github.com/angzarr-io/angzarr-client-go/proto/angzarr"
)

// Create a client
client := angzarr.NewClient("localhost:50051")

// Build and execute a command
response, err := client.Command("orders", rootUUID).
    SetCommand("CreateOrder", &CreateOrderCmd{...}).
    Execute(ctx)
```

## Development

### Prerequisites

- Go 1.21+
- Buf CLI for proto generation

### Proto Generation

```bash
buf generate
```

### Running Tests

```bash
go test ./...
```

### Running Tests with Coverage

```bash
go test -cover ./...
```

## License

Apache 2.0
