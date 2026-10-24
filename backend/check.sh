#!/bin/sh
# Backend checks. Agents run this before finishing; the orchestrator's quality gate runs it too.
set -e
cd "$(dirname "$0")"

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

echo "==> go test"
go test ./...
