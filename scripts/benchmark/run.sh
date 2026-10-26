#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${BENCHMARK_ENV_FILE:-$ROOT_DIR/benchmark/gcp.env}"
MODE=""
ROLE="all"
FRAMES=""
RUN_ID=""
RESULTS_DIR=""
MEASURE_PER_FRAME_LATENCY=false
SINK_PID=""
SOURCE_PID=""

usage() {
	echo "Usage: $0 --mode demo|benchmark|spatial-demo|spatial-benchmark [--role sink|source|all] [--env-file PATH] [--frames N] [--run-id ID] [--results DIR] [--measure-per-frame-latency]"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--mode) MODE="$2"; shift 2 ;;
		--role) ROLE="$2"; shift 2 ;;
		--env-file) ENV_FILE="$2"; shift 2 ;;
		--frames) FRAMES="$2"; shift 2 ;;
		--run-id) RUN_ID="$2"; shift 2 ;;
		--results) RESULTS_DIR="$2"; shift 2 ;;
		--measure-per-frame-latency) MEASURE_PER_FRAME_LATENCY=true; shift ;;
		-h|--help) usage; exit 0 ;;
		*) usage >&2; exit 1 ;;
	esac
done

[[ -n "$MODE" ]] || { usage >&2; exit 1; }
[[ "$ROLE" == sink || "$ROLE" == source || "$ROLE" == all ]] || { usage >&2; exit 1; }
[[ -f "$ENV_FILE" ]] || { printf 'Missing environment file: %s\n' "$ENV_FILE" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
FRAMES="${FRAMES:-${FRAME_FRAMES:-300}}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
RESULTS_DIR="${RESULTS_DIR:-$ROOT_DIR/results/$RUN_ID}"
mkdir -p "$RESULTS_DIR"
cd "$ROOT_DIR"

case "$MODE" in
	demo)
		SINK_TARGET=run-segmentation-demo-sink
		SOURCE_TARGET=run-segmentation-demo-source
		FUNCTION_NAME="${SEGMENTATION_DEMO_NAME:-segmentation-demo}"
		SINK_ARGS=(FRAME_PLAYBACK_FPS="${FRAME_PLAYBACK_FPS:-30}")
		;;
	benchmark)
		SINK_TARGET=run-segmentation-evaluation-sink
		SOURCE_TARGET=run-segmentation-evaluation-source
		FUNCTION_NAME="${SEGMENTATION_BENCHMARK_NAME:-segmentation-benchmark}"
		SINK_ARGS=(FRAME_FRAMES="$FRAMES" SEGMENTATION_BENCHMARK_METRICS="$RESULTS_DIR/sink-metrics.jsonl" SEGMENTATION_BENCHMARK_RUN_ID="$RUN_ID" SEGMENTATION_MEASURE_PER_FRAME_LATENCY="$MEASURE_PER_FRAME_LATENCY")
		;;
	spatial-demo)
		SINK_TARGET=run-segmentation-demo-sink
		SOURCE_TARGET=run-segmentation-spatial-demo-source
		FUNCTION_NAME="${SEGMENTATION_SCALE_SPLITTER_NAME:-segmentation-tilesplitter}"
		SINK_ARGS=(FRAME_PLAYBACK_FPS="${FRAME_PLAYBACK_FPS:-30}")
		;;
	spatial-benchmark)
		SINK_TARGET=run-segmentation-spatial-evaluation-sink
		SOURCE_TARGET=run-segmentation-spatial-evaluation-source
		FUNCTION_NAME="${SEGMENTATION_SCALE_SPLITTER_NAME:-segmentation-tilesplitter}"
		SINK_ARGS=(FRAME_FRAMES="$FRAMES" SEGMENTATION_SCALE_METRICS="$RESULTS_DIR/sink-metrics.jsonl" SEGMENTATION_SCALE_RUN_ID="$RUN_ID" SEGMENTATION_MEASURE_PER_FRAME_LATENCY="$MEASURE_PER_FRAME_LATENCY")
		;;
	*)
		printf 'Unsupported mode: %s\n' "$MODE" >&2
		exit 1
		;;
esac

cleanup() {
	[[ -z "$SOURCE_PID" ]] || kill_tree "$SOURCE_PID"
	[[ -z "$SINK_PID" ]] || kill_tree "$SINK_PID"
}

ensure_port_free() {
	local port="$1"
	local listeners
	if ! command -v lsof >/dev/null 2>&1; then
		return 0
	fi
	listeners="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)"
	if [[ -n "$listeners" ]]; then
		printf 'Port %s is already in use:\n%s\n' "$port" "$listeners" >&2
		printf 'Stop the listed process before starting this workflow.\n' >&2
		return 1
	fi
}

kill_tree() {
	local pid="$1"
	local child
	while read -r child; do
		[[ -z "$child" ]] || kill_tree "$child"
	done < <(pgrep -P "$pid" 2>/dev/null || true)
	kill "$pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

run_sink() {
	make "$SINK_TARGET" "${SINK_ARGS[@]}"
}

run_source_and_connect() {
	SOURCE_LOG="$RESULTS_DIR/source.log"
	make "$SOURCE_TARGET" FRAME_FRAMES="$FRAMES" \
		SEGMENTATION_BENCHMARK_RUN_ID="$RUN_ID" SEGMENTATION_SCALE_RUN_ID="$RUN_ID" \
		FRAME_SOURCE_METRICS="$RESULTS_DIR/source-metrics.jsonl" >"$SOURCE_LOG" 2>&1 &
	SOURCE_PID=$!
	for _ in {1..30}; do
		if grep -q "frame source listening" "$SOURCE_LOG" 2>/dev/null; then
			break
		fi
		if ! kill -0 "$SOURCE_PID" 2>/dev/null; then
			cat "$SOURCE_LOG" >&2
			return 1
		fi
		sleep 1
	done
	if ! grep -q "frame source listening" "$SOURCE_LOG" 2>/dev/null; then
		printf 'Source did not start listening; see %s\n' "$SOURCE_LOG" >&2
		return 1
	fi
	ATTACHMENT_REQUESTED_AT="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
	printf '{"type":"streaming_faas_activation","run_id":"%s","activation_trigger_at":"%s","activation_trigger_origin":"attachment request","attachment_requested_at":"%s"}\n' \
		"$RUN_ID" "$ATTACHMENT_REQUESTED_AT" "$ATTACHMENT_REQUESTED_AT" >"$RESULTS_DIR/activation-metrics.jsonl"
	make connect FUNCTION_NAME="$FUNCTION_NAME"
	wait "$SOURCE_PID"
	SOURCE_PID=""
}

case "$ROLE" in
	sink)
		ensure_port_free "${FRAME_SINK_PORT:-9100}"
		run_sink
		;;
	source)
		ensure_port_free "${CONNECT_PORT:-9000}"
		run_source_and_connect
		;;
	all)
		ensure_port_free "${FRAME_SINK_PORT:-9100}"
		ensure_port_free "${CONNECT_PORT:-9000}"
		SINK_LOG="$RESULTS_DIR/sink.log"
		make "$SINK_TARGET" "${SINK_ARGS[@]}" >"$SINK_LOG" 2>&1 &
		SINK_PID=$!
		for _ in {1..30}; do
			if grep -Eq "frame player listening|frame sink listening" "$SINK_LOG" 2>/dev/null; then
				break
			fi
			if ! kill -0 "$SINK_PID" 2>/dev/null; then
				cat "$SINK_LOG" >&2
				exit 1
			fi
			sleep 1
		done
		if ! grep -Eq "frame player listening|frame sink listening" "$SINK_LOG" 2>/dev/null; then
			printf 'Sink did not start; see %s\n' "$SINK_LOG" >&2
			exit 1
		fi
		run_source_and_connect
		wait "$SINK_PID" || true
		;;
esac
