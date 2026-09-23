#!/bin/sh
set -eu
generated_tmp=$(mktemp)
trap 'rm -f "$generated_tmp"' EXIT
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config=codegen.yaml -o "$generated_tmp" ../../packages/contracts/openapi.yaml
cmp -s "$generated_tmp" internal/httpapi/generated/api.gen.go || {
  echo 'Generated Go types differ; run pnpm generate.' >&2
  exit 1
}
