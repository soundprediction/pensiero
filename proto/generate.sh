#!/usr/bin/env bash
# Regenerate Go gRPC stubs. Requires protoc, protoc-gen-go, protoc-gen-go-grpc on PATH.
set -euo pipefail
cd "$(dirname "$0")/.."
protoc -I proto \
  --go_out=. --go_opt=module=github.com/soundprediction/pensiero \
  --go-grpc_out=. --go-grpc_opt=module=github.com/soundprediction/pensiero \
  proto/pensiero/v1/reasoning.proto
echo "generated pkg/grpcsvc/pb/*.pb.go"
