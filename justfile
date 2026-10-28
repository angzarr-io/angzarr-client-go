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
#   - Commands execute directly via `just --justfile justfile.container <target>`
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
        # Name the container justfile explicitly. A bare `just {{ARGS}}`
        # resolves to THIS (host) justfile, whose recipes call `_container`
        # again: build -> _container build -> just build -> ... with no limit.
        just --justfile "{{ROOT}}/justfile.container" {{ARGS}}
    else
        # Mount the shared git dir at its host path so a linked worktree
        # (whose .git file points there) resolves inside the container;
        # safe.directory lets git read a checkout owned by another uid.
        git_common="$(git rev-parse --path-format=absolute --git-common-dir)"
        # Rootless docker maps container uid 0 to the host user, so `-u 0:0`
        # makes writes to the mounted checkout land as the host user; the
        # image's default user maps to an unowned subuid there and cannot
        # write it. Rootful docker keeps the image default.
        user_flag=()
        if [[ "$(docker info --format '{{{{json .SecurityOptions}}')" == *rootless* ]]; then
            user_flag=(-u 0:0)
        fi
        docker run --rm --network=host "${user_flag[@]}" \
            -v "{{ROOT}}:/workspace:Z" \
            -v "${git_common}:${git_common}" \
            -e GIT_CONFIG_COUNT=1 \
            -e GIT_CONFIG_KEY_0=safe.directory \
            -e GIT_CONFIG_VALUE_0='*' \
            -v "{{ROOT}}/justfile.container:/workspace/justfile:ro" \
            -w /workspace \
            -e DEVCONTAINER=true \
            {{IMAGE}} just {{ARGS}}
    fi

default:
    @just --list

proto:
    just _container proto

build:
    just _container build

test:
    just _container test

# Start gRPC test server for unified Rust harness testing
serve:
    just _container serve

coverage:
    just _container coverage

# Show coverage in browser (host only)
coverage-html:
    just _container coverage
    go tool cover -html="{{ROOT}}/coverage.out"

mutate:
    just _container mutate

mutate-dry:
    just _container mutate-dry

clean:
    rm -f "{{ROOT}}/coverage.out"

# Check formatting
fmt:
    just _container fmt

# Auto-format code
fmt-fix:
    just _container fmt-fix
