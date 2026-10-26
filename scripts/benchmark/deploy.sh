#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${BENCHMARK_ENV_FILE:-$ROOT_DIR/benchmark/gcp.env}"
MODE=""
WORKERS=""
PASSES=""

usage() {
	echo "Usage: $0 --mode demo|benchmark|spatial-demo|spatial-benchmark [--env-file PATH] [--workers N] [--passes N]"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--mode) MODE="$2"; shift 2 ;;
		--env-file) ENV_FILE="$2"; shift 2 ;;
		--workers) WORKERS="$2"; shift 2 ;;
		--passes) PASSES="$2"; shift 2 ;;
		-h|--help) usage; exit 0 ;;
		*) usage >&2; exit 1 ;;
	esac
done

[[ -n "$MODE" ]] || { usage >&2; exit 1; }
[[ -f "$ENV_FILE" ]] || { printf 'Missing environment file: %s\n' "$ENV_FILE" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
WORKERS="${WORKERS:-${SEGMENTATION_SCALE_WORKERS:-4}}"
PASSES="${PASSES:-${SEGMENTATION_BENCHMARK_PASSES:-12}}"
cd "$ROOT_DIR"

case "$MODE" in
	demo)
		make deploy-segmentation-demo
		;;
	benchmark)
		make deploy-segmentation-benchmark SEGMENTATION_BENCHMARK_PASSES="$PASSES"
		;;
	spatial-demo)
		make deploy-segmentation-spatial-demo SEGMENTATION_SCALE_WORKERS="$WORKERS"
		;;
	spatial-benchmark)
		make deploy-segmentation-spatial-scale SEGMENTATION_SCALE_WORKERS="$WORKERS" SEGMENTATION_SCALE_PASSES="$PASSES"
		;;
	*)
		printf 'Unsupported mode: %s\n' "$MODE" >&2
		usage >&2
		exit 1
		;;
esac
