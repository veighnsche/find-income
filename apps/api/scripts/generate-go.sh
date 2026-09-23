#!/bin/sh
set -eu
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config=codegen.yaml -o internal/httpapi/generated/api.gen.go ../../packages/contracts/openapi.yaml
