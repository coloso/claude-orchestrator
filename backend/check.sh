#!/bin/sh
# Backend checks: generate drift, gofmt, vet, tests (incl. archtest), coverage, gosec, govulncheck.
# The checks need MongoDB at MONGO_TEST_URI (start one with `make mongo`); MongoDB tests fail without it.
# Agents run this before finishing; the orchestrator's quality gate runs it too.
set -e
cd "$(dirname "$0")"

if [ -z "${MONGO_TEST_URI:-}" ]; then
  echo "MONGO_TEST_URI is not set. The backend tests run against MongoDB:"
  echo "  make mongo"
  echo "  export MONGO_TEST_URI=mongodb://localhost:27017"
  exit 1
fi

# Minimum statement coverage for the inner layers (internal/<context>/domain and app).
MIN_COVERAGE=${MIN_COVERAGE:-80}

oas_sum() { find internal/oas -type f -name '*.go' | sort | xargs cat | shasum; }

echo "==> go generate (ogen)"
before=$(oas_sum)
go generate ./...
if [ "$before" != "$(oas_sum)" ]; then
  echo "internal/oas was out of sync with api/openapi.yaml and has been regenerated."
  echo "Never edit internal/oas by hand; run 'go generate ./...' after changing the spec, then check again."
  exit 1
fi

echo "==> gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "needs gofmt -w:"; echo "$unformatted"; exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test (includes internal/archtest dependency rules)"
if ! out=$(go test -cover ./... 2>&1); then
  echo "$out"; exit 1
fi
echo "$out"

echo "==> coverage >= ${MIN_COVERAGE}% for domain and app packages"
echo "$out" | awk -v min="$MIN_COVERAGE" '
  {
    pkg = ""
    for (i = 1; i <= NF; i++) if ($i ~ /\/internal\/[^\/]+\/(domain|app)$/) pkg = $i
    if (pkg == "") next
    cov = -1
    for (i = 1; i < NF; i++) if ($i == "coverage:") { cov = $(i + 1); sub("%", "", cov) }
    if (cov ~ /^\[no/) next   # "[no statements]": nothing to cover
    if (cov < 0 || cov + 0 < min) { printf "  %s: %s (want >= %s%%)\n", pkg, (cov < 0 ? "no tests" : cov "%"), min; bad = 1 }
  }
  END { exit bad }
' || { echo "Inner layers are built test-first: add the missing tests."; exit 1; }

echo "==> security: gosec (static analysis)"
go tool gosec -quiet -color=false -exclude-generated -exclude-dir=internal/oas ./...

echo "==> security: govulncheck (known vulnerabilities in called dependencies)"
go tool govulncheck ./...
