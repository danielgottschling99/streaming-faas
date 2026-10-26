#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${BENCHMARK_ENV_FILE:-$ROOT_DIR/benchmark/gcp.env}"
[[ -f "$ENV_FILE" ]] || { echo "Missing $ENV_FILE" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
: "${SUT_IP:?SUT_IP is required}"
: "${CLIENT_IP:?CLIENT_IP is required}"
: "${SERVER_URL:?SERVER_URL is required}"
for command in go make nc; do
	command -v "$command" >/dev/null || { echo "Missing command: $command" >&2; exit 1; }
done
if ! nc -z -w 3 "$SUT_IP" 8080; then
	echo "Cannot reach FaaS control plane at $SUT_IP:8080" >&2
	exit 1
fi
echo "Benchmark preflight passed: SUT=$SUT_IP CLIENT=$CLIENT_IP API=$SERVER_URL"
echo "Source and sink ports are checked when their processes start."
