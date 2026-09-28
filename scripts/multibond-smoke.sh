#!/usr/bin/env bash
set -euo pipefail

echo "== OpenFlux MultiBond smoke =="
echo "Go: $(go version)"

echo "== gofmt check =="
bad="$(gofmt -l transport/bond transport/session_bond.go transport/session_bond_test.go)"
if [[ -n "$bad" ]]; then
  echo "Files needing gofmt:"
  echo "$bad"
  exit 1
fi

echo "== scheduler tests =="
go test ./transport/bond -count=1

echo "== Session tests =="
go test ./transport -run 'TestSession|Bond' -count=1

echo "== full unit suite =="
go test ./... -count=1

echo "== vet =="
go vet ./...

echo "== build current platform =="
go build -o /tmp/openflux-multibond-smoke .

echo "== cross-build Linux amd64 =="
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/openflux-linux-amd64 .

echo "== cross-build Linux arm64 =="
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/openflux-linux-arm64 .

echo "== cross-build macOS arm64 =="
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/openflux-darwin-arm64 .

echo "== cross-build Windows amd64 =="
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/openflux-windows-amd64.exe .

echo "OK: MultiBond smoke passed"
