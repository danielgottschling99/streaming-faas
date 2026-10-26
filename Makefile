.PHONY: help start deploy connect \
	deploy-segmentation-demo deploy-segmentation-benchmark \
	deploy-segmentation-spatial-scale deploy-segmentation-spatial-demo \
	run-segmentation-demo-sink \
	run-segmentation-demo-source run-segmentation-evaluation-sink \
	run-segmentation-evaluation-source run-segmentation-evaluation \
	run-segmentation-spatial-evaluation-sink run-segmentation-spatial-evaluation-source \
	run-segmentation-spatial-evaluation run-segmentation-spatial-demo-source \
	run-segmentation-spatial-demo deploy-colorfilter-watermark-demo clean clean-generated

SERVER_URL ?= http://localhost:8080
FUNCTION_NAME ?= segmentation-demo
FUNCTION_DIR ?= ./examples/segmentation-demo
CONNECT_HOST ?= host.docker.internal
CONNECT_PORT ?= 9000

FRAME_SOURCE_LISTEN ?= 0.0.0.0:9000
FRAME_SINK_LISTEN ?= 0.0.0.0:9100
FRAME_SINK_HOST ?= host.docker.internal
FRAME_SINK_PORT ?= 9100
FRAME_SINK_SAVE_DIR ?=
FRAME_FRAMES ?= 300
FRAME_SOURCE_METRICS ?=
FRAME_MODE ?= burst
FRAME_FPS ?= 30

SEGMENTATION_DEMO_NAME ?= segmentation-demo
SEGMENTATION_DEMO_INPUT ?= frame-input-low
SEGMENTATION_DEMO_FRAMES ?= 3600
FRAME_PLAYBACK_FPS ?= 30

SEGMENTATION_BENCHMARK_NAME ?= segmentation-benchmark
SEGMENTATION_BENCHMARK_INPUT ?= frame-input
SEGMENTATION_BENCHMARK_PASSES ?= 12
SEGMENTATION_BENCHMARK_METRICS ?= segmentation-sink-metrics.jsonl
SEGMENTATION_MEASURE_PER_FRAME_LATENCY ?= false
SEGMENTATION_BENCHMARK_RUN_ID ?= segmentation-benchmark

SEGMENTATION_SCALE_SPLITTER_NAME ?= segmentation-tilesplitter
SEGMENTATION_SCALE_FUSER_NAME ?= segmentation-tilefuser
SEGMENTATION_SCALE_WORKERS ?= 4
SEGMENTATION_SCALE_PASSES ?= 12
SEGMENTATION_SCALE_DEMO_INPUT ?= frame-input-low
SEGMENTATION_SCALE_DEMO_FRAMES ?= 3600
SEGMENTATION_SCALE_RUN_ID ?= segmentation-spatial
SEGMENTATION_SCALE_METRICS ?= segmentation-spatial-sink-metrics.jsonl
FRAME_SCALE_HANDLER_HOST ?= host.docker.internal
FRAME_SCALE_HANDLER_PORT ?= 8080
FRAME_SCALE_OUTPUT_HOST ?= host.docker.internal
FRAME_SCALE_OUTPUT_PORT ?= 9100

help:
	@echo "Frame-processing workflows:"
	@echo "  make start                                  Start the FaaS server"
	@echo "  make deploy-segmentation-demo              Deploy fast single-worker demo"
	@echo "  make deploy-segmentation-benchmark         Deploy slow single-worker benchmark"
	@echo "  make deploy-segmentation-spatial-demo      Deploy fast spatial demo"
	@echo "  make deploy-segmentation-spatial-scale     Deploy slow spatial benchmark"
	@echo "  make deploy-colorfilter-watermark-demo     Deploy color filter -> PNG watermark"
	@echo "  make run-segmentation-demo-sink            Start ffplay sink"
	@echo "  make run-segmentation-demo-source          Start fast demo source"
	@echo "  make run-segmentation-evaluation-sink      Start metrics sink"
	@echo "  make run-segmentation-evaluation-source    Start benchmark source"
	@echo "  make run-segmentation-spatial-evaluation-sink   Start scaled metrics sink"
	@echo "  make run-segmentation-spatial-evaluation-source Start scaled source"
	@echo "  make connect FUNCTION_NAME=...             Connect a deployed function"

start:
	go run main.go

deploy:
	go run cmd/deploy/main.go -name $(FUNCTION_NAME) -dir $(FUNCTION_DIR) -server $(SERVER_URL)

connect:
	go run cmd/connect/main.go -name $(FUNCTION_NAME) -host $(CONNECT_HOST) -port $(CONNECT_PORT) -server $(SERVER_URL)

deploy-colorfilter-watermark-demo:
	go run cmd/deploy/main.go -name watermark -dir ./examples/watermark -server $(SERVER_URL) \
		-env BINARY_MODE=true,OUTPUT_HOST=$(FRAME_SINK_HOST),OUTPUT_PORT=$(FRAME_SINK_PORT)
	go run cmd/deploy/main.go -name colorfilter -dir ./examples/colorfilter -server $(SERVER_URL) \
		-env BINARY_MODE=true,OUTPUT_HANDLER_HOST=$(FRAME_SCALE_HANDLER_HOST),OUTPUT_HANDLER_PORT=$(FRAME_SCALE_HANDLER_PORT),OUTPUT_HANDLER_NAME=watermark

deploy-segmentation-demo:
	go run cmd/deploy/main.go -name $(SEGMENTATION_DEMO_NAME) -dir ./examples/segmentation-demo -server $(SERVER_URL) \
		-env BINARY_MODE=true,OUTPUT_HOST=$(FRAME_SINK_HOST),OUTPUT_PORT=$(FRAME_SINK_PORT)

deploy-segmentation-benchmark:
	go run cmd/deploy/main.go -name $(SEGMENTATION_BENCHMARK_NAME) -dir ./examples/segmentation-demo -server $(SERVER_URL) \
		-env BINARY_MODE=true,OUTPUT_HOST=$(FRAME_SINK_HOST),OUTPUT_PORT=$(FRAME_SINK_PORT),SEGMENTATION_REFINEMENT_PASSES=$(SEGMENTATION_BENCHMARK_PASSES)

deploy-segmentation-spatial-scale:
	@set -e; \
	workers="$(SEGMENTATION_SCALE_WORKERS)"; \
	if [ "$$workers" -lt 1 ] || [ "$$((workers & (workers - 1)))" -ne 0 ]; then \
		echo "SEGMENTATION_SCALE_WORKERS must be a positive power of two (got $$workers)" >&2; exit 1; \
	fi; \
	names=""; i=0; \
	while [ "$$i" -lt "$$workers" ]; do \
		if [ -n "$$names" ]; then names="$$names,"; fi; \
		names="$${names}segmentation-tile$$i"; i=$$((i + 1)); \
	done; \
	go run cmd/deploy/main.go -name $(SEGMENTATION_SCALE_FUSER_NAME) -dir ./examples/frametilefuser -server $(SERVER_URL) \
		-env BINARY_MODE=true,INPUT_ACCEPT_MODE=multi,INPUT_EXPECTED_CONNECTIONS=$$workers,FUSER_INPUT_NAMES=$$names,FRAME_TILE_WORKERS=$$workers,OUTPUT_HOST=$(FRAME_SCALE_OUTPUT_HOST),OUTPUT_PORT=$(FRAME_SCALE_OUTPUT_PORT); \
	i=0; while [ "$$i" -lt "$$workers" ]; do \
		go run cmd/deploy/main.go -name segmentation-tile$$i -dir ./examples/segmentation-demo -server $(SERVER_URL) \
			-env BINARY_MODE=true,SEGMENTATION_REFINEMENT_PASSES=$(SEGMENTATION_SCALE_PASSES),OUTPUT_HANDLER_HOST=$(FRAME_SCALE_HANDLER_HOST),OUTPUT_HANDLER_PORT=$(FRAME_SCALE_HANDLER_PORT),OUTPUT_HANDLER_NAME=$(SEGMENTATION_SCALE_FUSER_NAME); \
		i=$$((i + 1)); \
	done; \
	go run cmd/deploy/main.go -name $(SEGMENTATION_SCALE_SPLITTER_NAME) -dir ./examples/frametilesplitter -server $(SERVER_URL) \
		-env BINARY_MODE=true,FRAME_TILE_WORKERS=$$workers,OUTPUT_HANDLER_HOST=$(FRAME_SCALE_HANDLER_HOST),OUTPUT_HANDLER_PORT=$(FRAME_SCALE_HANDLER_PORT),OUTPUT_HANDLER_NAMES=$$names

deploy-segmentation-spatial-demo:
	$(MAKE) deploy-segmentation-spatial-scale SEGMENTATION_SCALE_PASSES=0

run-segmentation-demo-sink:
	go run ./cmd/frame-player -listen $(FRAME_SINK_LISTEN) -fps $(FRAME_PLAYBACK_FPS)

run-segmentation-demo-source:
	go run ./cmd/frame-source -listen $(FRAME_SOURCE_LISTEN) -input $(SEGMENTATION_DEMO_INPUT) -frames $(SEGMENTATION_DEMO_FRAMES) -mode steady -fps $(FRAME_FPS) -run-id segmentation-demo -out ""

run-segmentation-evaluation-sink:
	go run ./cmd/frame-sink -listen $(FRAME_SINK_LISTEN) -frames $(FRAME_FRAMES) -run-id $(SEGMENTATION_BENCHMARK_RUN_ID) -out $(SEGMENTATION_BENCHMARK_METRICS) -save-dir "$(FRAME_SINK_SAVE_DIR)" -measure-per-frame-latency=$(SEGMENTATION_MEASURE_PER_FRAME_LATENCY)

run-segmentation-evaluation-source:
	go run ./cmd/frame-source -listen $(FRAME_SOURCE_LISTEN) -input $(SEGMENTATION_BENCHMARK_INPUT) -frames $(FRAME_FRAMES) -mode $(FRAME_MODE) -fps $(FRAME_FPS) -run-id $(SEGMENTATION_BENCHMARK_RUN_ID) -out "$(FRAME_SOURCE_METRICS)"

run-segmentation-evaluation:
	@set -e; \
	$(MAKE) run-segmentation-evaluation-source & source_pid=$$!; \
	trap 'kill $$source_pid 2>/dev/null || true' EXIT INT TERM; \
	sleep 1; \
	$(MAKE) connect FUNCTION_NAME=$(SEGMENTATION_BENCHMARK_NAME); \
	wait $$source_pid

run-segmentation-spatial-evaluation-sink:
	go run ./cmd/frame-sink -listen $(FRAME_SINK_LISTEN) -frames $(FRAME_FRAMES) -run-id $(SEGMENTATION_SCALE_RUN_ID) -out $(SEGMENTATION_SCALE_METRICS) -save-dir "$(FRAME_SINK_SAVE_DIR)" -measure-per-frame-latency=$(SEGMENTATION_MEASURE_PER_FRAME_LATENCY)

run-segmentation-spatial-evaluation-source:
	go run ./cmd/frame-source -listen $(FRAME_SOURCE_LISTEN) -input $(SEGMENTATION_BENCHMARK_INPUT) -frames $(FRAME_FRAMES) -mode $(FRAME_MODE) -fps $(FRAME_FPS) -run-id $(SEGMENTATION_SCALE_RUN_ID) -out "$(FRAME_SOURCE_METRICS)"

run-segmentation-spatial-evaluation:
	@set -e; \
	$(MAKE) run-segmentation-spatial-evaluation-source & source_pid=$$!; \
	trap 'kill $$source_pid 2>/dev/null || true' EXIT INT TERM; \
	sleep 1; \
	$(MAKE) connect FUNCTION_NAME=$(SEGMENTATION_SCALE_SPLITTER_NAME); \
	wait $$source_pid

run-segmentation-spatial-demo-source:
	go run ./cmd/frame-source -listen $(FRAME_SOURCE_LISTEN) -input $(SEGMENTATION_SCALE_DEMO_INPUT) -frames $(SEGMENTATION_SCALE_DEMO_FRAMES) -mode steady -fps $(FRAME_FPS) -run-id segmentation-spatial-demo -out ""

run-segmentation-spatial-demo:
	@set -e; \
	$(MAKE) run-segmentation-spatial-demo-source & source_pid=$$!; \
	trap 'kill $$source_pid 2>/dev/null || true' EXIT INT TERM; \
	sleep 1; \
	$(MAKE) connect FUNCTION_NAME=$(SEGMENTATION_SCALE_SPLITTER_NAME); \
	wait $$source_pid

clean-generated:
	rm -rf ./tmp/* ./frame-output/*
	rm -f -- *.jsonl

clean: clean-generated
