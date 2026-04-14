> **⚠️ Out of Date:** This repository is currently out of date. Primary development focus is on the **Rust** and **Python** implementations. The author will get back to updating this, but if you need it sooner, please [open an issue](https://github.com/angzarr-io/angzarr/issues) or contact the author directly.

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

BSD-3-Clause

## Development

### Setup

Install git hooks (requires [lefthook](https://github.com/evilmartians/lefthook)):

```bash
lefthook install
```

This configures a pre-commit hook that auto-formats code before each commit.

### Recipes

```bash
just -l              # List all available recipes
just build           # Build the library
just test            # Run tests
just fmt             # Check formatting
just fmt-fix         # Auto-format code
```
