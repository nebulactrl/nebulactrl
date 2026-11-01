#!/usr/bin/env bash
# Fails when golang.org/x/tools/cmd/deadcode finds a function that no entry point reaches.
#
# Two passes, both GOOS=linux (what ships and what CI runs on, so the result does
# not depend on the developer's OS):
#
# 1. Production code. The roots are the main packages only (no -test), so a
#    function that nothing but a test calls is reported: it is not shipped
#    behaviour, and a test of it only keeps it alive. The test-support packages
#    (SUPPORT_RE) are not reported here: they are non-_test.go files that only
#    tests import, so they are all unreachable from the mains by construction.
# 2. Test-support code. The roots are the mains plus every test (-test, with the
#    build tags that gate test files), and only findings in SUPPORT_RE are
#    reported: a helper that no test calls is dead.
#
# Generated code is not deleted but ignored: deadcode skips files with a
# "Code generated" header by itself, and EXCLUDE_RE covers generated output that
# lacks one. Add a pattern to a regex below (with a reason) rather than keeping
# dead code.
#
# Usage: scripts/ci/deadcode.sh
set -euo pipefail

DEADCODE_VERSION="v0.52.0"
# Build tags that gate test files: integration (tests/integration), instsmoke
# (internal/instsvc/forward). Only pass 2 sees test files.
TAGS="integration,instsmoke"

# Extended regexes matched against the file path of each finding.
# Generated output without a "Code generated" header.
EXCLUDE_RE='^(internal/db/|internal/apiclient/|.*\.gen\.go:|.*\.pb\.go:|.*_gen\.go:)'
# Test-support packages: internal/testutil, the internal/**/<name>test helper
# packages (meshtest, gitsshtest, passkeytest, ...) and internal/templates/testsample.
SUPPORT_RE='^internal/(testutil/|templates/testsample/|([a-z0-9]+/)*[a-z0-9]*test/)'

cd "$(git rev-parse --show-toplevel)"

# Built here (not `go run`) because `go run` would compile deadcode for GOOS=linux,
# which cannot execute on a macOS workstation.
bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
GOBIN="$bin" go install "golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION}"

prod="$(GOOS=linux GOARCH=amd64 "$bin/deadcode" ./...)"
support="$(GOOS=linux GOARCH=amd64 "$bin/deadcode" -test -tags "$TAGS" ./...)"

findings="$(
  {
    grep -Ev "$EXCLUDE_RE" <<<"$prod" | grep -Ev "$SUPPORT_RE" || true
    grep -Ev "$EXCLUDE_RE" <<<"$support" | grep -E "$SUPPORT_RE" || true
  } | sort -u
)"
if [[ -n "$findings" ]]; then
  echo "$findings"
  echo
  echo "deadcode: $(wc -l <<<"$findings" | tr -d ' ') unreachable function(s). Delete them, or, if reached only through" >&2
  echo "something the call graph cannot see, say why in scripts/ci/deadcode.sh." >&2
  exit 1
fi
echo "deadcode: no unreachable functions"
