# Go client library commands
#
# Container Overlay Pattern:
# --------------------------
# This justfile uses an overlay pattern for container execution:
#
# 1. `justfile` (this file) - runs on the host, delegates to container
# 2. `justfile.container` - mounted over this file inside the container
#
# When running outside a devcontainer:
#   - Uses pre-built angzarr-go image from ghcr.io/angzarr
#   - Podman mounts justfile.container as /workspace/justfile
#
# When running inside a devcontainer (DEVCONTAINER=true):
#   - Commands execute directly via `just <target>`
#   - No container nesting

set shell := ["bash", "-c"]

# Reusable submodule-protection recipes (install-submodule-hooks,
# check-submodules-clean). Source of truth: angzarr-project/submodule.just.
import? 'angzarr-project/submodule.just'

ROOT := `git rev-parse --show-toplevel`
IMAGE := "ghcr.io/angzarr-io/angzarr-go:latest"

# Run just target in container (or directly if already in devcontainer)
[private]
_container +ARGS:
    #!/usr/bin/env bash
    if [ "${DEVCONTAINER:-}" = "true" ]; then
        just {{ARGS}}
    else
        docker run --rm --network=host \
            -v "{{ROOT}}:/workspace:Z" \
            -v "{{ROOT}}/justfile.container:/workspace/justfile:ro" \
            -w /workspace \
            -e DEVCONTAINER=true \
            {{IMAGE}} just {{ARGS}}
    fi

# Run a mutation-testing target with the workspace mounted READ-ONLY.
#
# WHY:
#   The mutation suite (ooze) symlinks source files into a tmpdir then
#   os.WriteFile-overwrites the symlink path (so the rewrite lands in the
#   tmpdir, not the original file). In the current ooze version this leaves
#   the host tree alone — BUT (a) custom viruses or a future ooze release
#   could rewrite source in place, and (b) `proto`/`go mod tidy` invoked
#   from inside `mutation-test` mutate go.mod/go.sum and write *.pb.go
#   files. Either way the mount is RW today and a crashed run would leak.
#   This helper closes both holes: source is mounted at /src:ro, copied
#   into /work inside the container's WRITABLE OVERLAY LAYER, and `--rm`
#   destroys the overlay (and every byte ooze touched) on exit.
#
# WHAT TOUCHES THE HOST:
#   - {{ROOT}}/.mutants-cache/{go-pkg,go-build} — Go module/build caches
#     only. NEVER contains mutated source files. Gitignored. Delete the
#     dir to purge the cache.
#   - {{ROOT}}/mutation-results/mutation-test.log — tee'd combined output.
#
# WHAT NEVER TOUCHES THE HOST:
#   - Mutated source trees (live in /work, container overlay, --rm wipes).
#   - ooze's tmpdir symlink farms and overwritten files (live in $TMPDIR
#     inside the container).
#   - go mod tidy / buf generate edits to go.mod / *.pb.go (live in /work).
[private]
_container-ephemeral +ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "${DEVCONTAINER:-}" = "true" ]; then
        # Already inside a devcontainer — that container IS the ephemeral
        # boundary. Run directly; the outer just wrapper ensures --rm.
        just {{ARGS}}
        exit 0
    fi
    mkdir -p "{{ROOT}}/mutation-results" \
             "{{ROOT}}/.mutants-cache/go-pkg" \
             "{{ROOT}}/.mutants-cache/go-build"
    docker run --rm --network=host \
        -v "{{ROOT}}:/src:ro,Z" \
        -v "{{ROOT}}/mutation-results:/out:Z" \
        -v "{{ROOT}}/.mutants-cache/go-pkg:/go-pkg:Z" \
        -v "{{ROOT}}/.mutants-cache/go-build:/go-build:Z" \
        -v "{{ROOT}}/justfile.container:/etc/angzarr-justfile:ro" \
        -e DEVCONTAINER=true \
        -e GOPATH=/go-pkg \
        -e GOCACHE=/go-build \
        -e GOMODCACHE=/go-pkg/mod \
        -e MUTANTS_EPHEMERAL=1 \
        -w /work \
        {{IMAGE}} bash -eu -o pipefail -c '
            echo "[ephemeral] copying /src -> /work (container overlay)"
            mkdir -p /work
            # tar|tar: excludes mirror what we never want in the working
            # copy — vendored deps, the mutants cache itself, prior
            # mutation output, and editor crud.
            tar -C /src \
                --exclude=./vendor \
                --exclude=./.mutants-cache \
                --exclude=./mutation-results \
                --exclude=./coverage.out \
                -cf - . \
                | tar -C /work -xf -
            # Mount the container-side justfile into the copy so `just`
            # finds it (the original /src is read-only, but /work is
            # writable).
            cp /etc/angzarr-justfile /work/justfile
            cd /work
            just {{ARGS}} 2>&1 | tee /out/mutation-test.log
        '

default:
    @just --list

# =============================================================================
# Proto generation — cross-language model (project_proto_generation_model)
# =============================================================================
# `.proto` sources live in the angzarr-project submodule. Bindings are NEVER
# committed (see .gitignore: proto/**/*pb.go). They are regenerated:
#   1. on `post-checkout` / `post-merge` via lefthook (covers fresh clones,
#      branch switches, submodule bumps)
#   2. transparently as a recipe dependency of `build`, `test`, `fmt`, etc.
#      The recipe is idempotent — mtime guard skips when bindings are newer
#      than the newest .proto source.
#
# Runs in the same devcontainer image used for build/test/mutation so the
# buf + protoc-gen-go toolchain is fixed (no host fallback). Rootless docker
# requires `-u 0:0` per feedback_docker_rootless.
#
# Tool integration (no `//go:generate` for the pre-build trigger): regen
# orchestration stays in `just` so the same invocation pattern works across
# all 6 langs. Plain `go build` consumes the pre-emitted proto/**/*.pb.go.

PROTO_SRC_DIR := ROOT + "/angzarr-project/proto"
PROTO_OUT_DIR := ROOT + "/proto"

# Public entry point. Idempotent: returns immediately if bindings are
# fresher than the newest .proto source.
generate-proto:
    #!/usr/bin/env bash
    set -euo pipefail
    src_dir="{{PROTO_SRC_DIR}}"
    out_dir="{{PROTO_OUT_DIR}}"
    if [ ! -d "$src_dir" ]; then
        echo "[generate-proto] $src_dir missing — is the angzarr-project submodule initialized?" >&2
        exit 1
    fi
    # Staleness check: regenerate if any .proto file is newer than the
    # NEWEST generated binding, or if no bindings exist yet.
    # Catches "submodule bumped" and "fresh clone" — the hot paths driving
    # the lefthook trigger. Does NOT catch manual deletion of one binding
    # while others remain fresh; use `just generate-proto-force` for that.
    # Glob `*.pb.go` covers both protocolbuffers/go and grpc/go output
    # (the latter emits `*_grpc.pb.go`).
    #
    # NEWEST (not OLDEST as in Python/Rust) because the Go tree has stale
    # orphan .pb.go files from earlier buf configs (`proto/angzarr/`,
    # `proto/examples/`, `proto/io/`) that are not regenerated by the
    # current buf.gen.yaml. Using OLDEST would never converge. Cleaning
    # those orphans is the queued leaf-subpkg-dodge migration — out of
    # scope here. Semantic effect of NEWEST: when buf adds a brand-new
    # binding file we may skip regen if other bindings post-date the
    # newest .proto, but this never happens on a real submodule bump
    # because new sources have newer mtimes than any pre-existing binding.
    newest_proto=$(find "$src_dir" -name '*.proto' -printf '%T@\n' 2>/dev/null \
                    | sort -n | tail -1)
    newest_pb=$(find "$out_dir" -name '*.pb.go' -printf '%T@\n' 2>/dev/null \
                    | sort -n | tail -1)
    if [ -n "$newest_proto" ] && [ -n "$newest_pb" ] \
        && awk -v p="$newest_proto" -v b="$newest_pb" 'BEGIN{exit !(b>p)}'; then
        echo "[generate-proto] bindings up-to-date, skipping (use 'just generate-proto-force' to override)"
        exit 0
    fi
    just generate-proto-force

# Always regenerate, ignoring mtimes. Invoked by `generate-proto` when stale
# and exposed directly for users who want to force a rebuild.
generate-proto-force:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "${DEVCONTAINER:-}" = "true" ]; then
        # Inside the devcontainer image already — run directly.
        just --justfile "{{ROOT}}/justfile.container" generate-proto-force
        exit 0
    fi
    # Rootless docker: -u 0:0 maps to host user via subuid; writes to the
    # bind-mount land owned by the host user. Rootful: direct uid match.
    # See feedback_docker_rootless.
    if docker info --format '{{{{.SecurityOptions}}}}' 2>/dev/null | grep -q rootless; then
        USER_FLAG="-u 0:0"
    else
        USER_FLAG="-u $(id -u):$(id -g)"
    fi
    docker run --rm --network=host \
        $USER_FLAG \
        -v "{{ROOT}}:/workspace:Z" \
        -v "{{ROOT}}/justfile.container:/workspace/justfile:ro" \
        -w /workspace \
        -e DEVCONTAINER=true \
        {{IMAGE}} just generate-proto-force

# Legacy alias — kept so existing recipe-deps and muscle memory keep working.
proto: generate-proto

build: generate-proto
    just _container build

test: generate-proto
    just _container test

# Start gRPC test server for unified Rust harness testing
serve: generate-proto
    just _container serve

coverage: generate-proto
    just _container coverage

# Show coverage in browser (host only)
coverage-html: generate-proto
    just _container coverage
    go tool cover -html="{{ROOT}}/coverage.out"

# Run mutation tests in an ephemeral container (source mounted read-only).
# All mutations live in the container overlay and die with `--rm`.
# Host go-test/ooze invocations are FORBIDDEN — see _container-ephemeral.
mutation-test: generate-proto
    just _container-ephemeral mutation-test

# Purge the local mutation build cache (.mutants-cache/ — Go caches only).
mutants-purge-cache:
    rm -rf "{{ROOT}}/.mutants-cache"
    @echo "Removed {{ROOT}}/.mutants-cache"

# Wipe all generated and build artifacts (idempotent).
#
# Cross-language convention: every lang's justfile ships a `just clean`
# that nukes everything gitignored — generated proto bindings, build
# outputs, coverage/test reports, mutation caches. Safe to run any time;
# `just generate-proto-force` regenerates bindings, `just test` rebuilds.
clean:
    @echo "[clean] wiping generated proto bindings…"
    find "{{ROOT}}/proto" -name '*.pb.go' -delete 2>/dev/null || true
    @echo "[clean] wiping coverage + test artifacts…"
    rm -f "{{ROOT}}/coverage.out"
    find "{{ROOT}}" -maxdepth 2 -name '*.test' -type f -delete 2>/dev/null || true
    @echo "[clean] wiping mutation caches + reports…"
    rm -rf "{{ROOT}}/.mutants-cache" \
           "{{ROOT}}/mutation-results" \
           "{{ROOT}}/mutants-reports"
    @echo "[clean] wiping stray sub-workspace clones…"
    rm -rf "{{ROOT}}/angzarr"
    @echo "[clean] complete"

# Check formatting
fmt: generate-proto
    just _container fmt

# Auto-format code
fmt-fix: generate-proto
    just _container fmt-fix

# Cross-language alias — `just check` runs fmt check.
check: fmt
