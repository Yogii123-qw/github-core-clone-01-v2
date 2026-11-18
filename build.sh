#!/bin/sh
# Build the statically linked forge binary from src/ into the
# repository root. The committed binary is what the trial runs; this
# script reproduces it. Override the toolchain with GO=/path/to/go.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
GO=${GO:-go}
cd "$ROOT/src"
CGO_ENABLED=0 "$GO" build -trimpath -ldflags="-s -w -buildid=" -o "$ROOT/forge" .
ls -l "$ROOT/forge"
