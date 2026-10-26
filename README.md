# Streaming FaaS

A Go server that deploys functions in Docker containers and connects them to
TCP streams. The included workload segments JPEG frames into foreground masks.
The scaled version splits each frame into tiles, processes them in parallel,
and merges the results.

## Install and prepare

- **Go 1.24 or newer**, **Git**, and **Make**.
- **Docker** with its daemon running and permission to access it.
- **FFmpeg with `ffplay`** for the visual demonstration. The metrics benchmarks
  do not need `ffplay` or a graphical display.

On macOS, use `brew install go make ffmpeg` and install/start Docker Desktop.
On Linux, install the tools through your package manager. Check that your Go
version is recent enough.

```bash
git clone https://github.com/danielgottschling99/streaming-faas.git
cd streaming-faas
go mod download
docker info
```

Run all commands below from the repository root. Initial deployment downloads
the function's Docker base image and builds it, so internet access is needed.
Input frames are included in `frame-input` and `frame-input-low`.

The defaults use Docker Desktop's `host.docker.internal` to reach processes on
the host. On Linux, set these variables in the terminals running deployment
and connection commands to an IP address of your host reachable from Docker:

```bash
export CONNECT_HOST='<host-ip>' FRAME_SINK_HOST='<host-ip>'
export FRAME_SCALE_HANDLER_HOST='<host-ip>' FRAME_SCALE_OUTPUT_HOST='<host-ip>'
```

## Start the server

Keep this running in terminal 1 for all workflows:

```bash
make start
```

The API listens on port 8080. The source and sink use ports 9000 and 9100.
`make` uses `go run`, so no separate build is required.

## Demonstration at 8 FPS

In terminal 2, deploy the function and start the video sink:

```bash
make deploy-segmentation-demo
FRAME_PLAYBACK_FPS=8 make run-segmentation-demo-sink
```

In terminal 3, start the source:

```bash
FRAME_FPS=8 make run-segmentation-demo-source
```

Once the source reports that it is listening, connect the function in terminal 4:

```bash
make connect FUNCTION_NAME=segmentation-demo
```

The demo repeats the low-resolution inputs for 3,600 frames: about 7.5 minutes
at 8 FPS. Set `SEGMENTATION_DEMO_FRAMES` on the source command to change the
length. `FRAME_FPS` controls sending speed. `FRAME_PLAYBACK_FPS` controls playback.

## Two-function demonstration

This chain applies a sepia color filter, then overlays
[3S.png](examples/watermark/3S.png) in the bottom-right corner:
source → colorfilter → watermark → video sink.
With the server running, use terminal 2:

```bash
make deploy-colorfilter-watermark-demo
FRAME_PLAYBACK_FPS=8 make run-segmentation-demo-sink
```

Start `FRAME_FPS=8 make run-segmentation-demo-source` in terminal 3. Once it
is listening, run this in terminal 4:

```bash
make connect FUNCTION_NAME=colorfilter
```

The color filter automatically connects to the watermark function. The chain
uses the same frame count and input settings as the segmentation demo.
Redeploy both functions with the deployment target before repeating the run.
The PNG is embedded during compilation. Replace `examples/watermark/3S.png`
and redeploy to use a different watermark image.

## Single-worker benchmark

With the server running, deploy and start the metrics sink in terminal 2:

```bash
make deploy-segmentation-benchmark
make run-segmentation-evaluation-sink
```

In terminal 3, start the source and connect it automatically:

```bash
make run-segmentation-evaluation
```

The default repeats the 30 full-resolution inputs for 300 frames, sends them
as fast as possible, and uses 12 deliberately expensive refinement passes.
Change the work with `SEGMENTATION_BENCHMARK_PASSES` when deploying. Results
are appended to `segmentation-sink-metrics.jsonl`, including effective FPS,
received/missing/duplicate/malformed frame counts, and JPEG decode errors.
Set `SEGMENTATION_MEASURE_PER_FRAME_LATENCY=true` on the sink command to collect
per-frame end-to-end, processing, decode, transformation, and encode timings.

## Scaled benchmark

The pipeline is source → splitter → segmentation workers → fuser → metrics sink.
In terminal 2:

```bash
make deploy-segmentation-spatial-scale SEGMENTATION_SCALE_WORKERS=4
make run-segmentation-spatial-evaluation-sink
```

In terminal 3:

```bash
make run-segmentation-spatial-evaluation
```

Results are appended to `segmentation-spatial-sink-metrics.jsonl`. Worker counts
must be positive powers of two (for example 2, 4, 8, or 16), and input dimensions
must divide evenly into the tile grid. Change the work per tile with
`SEGMENTATION_SCALE_PASSES` when deploying.

The splitter preserves frame IDs and sends equal tiles to named workers. The
fuser waits for every tile of a frame and emits assembled frames in ID order.
Segmentation uses each tile's local context, so masks can differ from the
single-worker result and show seams or artifacts. Overlapping tiles or a
global cleanup pass would improve mask quality but require extra processing.
Scaled timing fields aggregate worker timestamps and include fusion in the
overall processing interval. They are not individual worker measurements.

For either benchmark, set `FRAME_FRAMES` to the same value on both sink and
source commands for a shorter or longer run. Use `FRAME_MODE=steady FRAME_FPS=8`
on the source command to benchmark a paced stream. Set `FRAME_SINK_SAVE_DIR` on
the sink command to save received JPEGs.

Stop host processes with Ctrl+C. Before repeating a workflow, restart its sink
and source and redeploy the function(s), especially the scaled pipeline. After
restarting the server, redeploy because its function registry is held in memory.
Stopping the server does not automatically remove function containers.

## Workload and frame protocol

Segmentation excludes black side padding, finds warm/dark foreground seeds,
keeps the component nearest the image center, flood-fills it, and dilates
twice. It emits a binary grayscale JPEG mask. The benchmark adds configurable
5×5 refinement passes. The fast demo uses zero passes. For a scaled visual
demo, use `make deploy-segmentation-spatial-demo`, the demo sink, and
`FRAME_FPS=8 make run-segmentation-spatial-demo`.

Each TCP frame has a 52-byte header followed by its JPEG payload:

```text
uint32 payload_length
uint64 frame_id
uint64 source_sent_at_unix_nanos
uint64 processing_started_at_unix_nanos
uint64 decode_finished_at_unix_nanos
uint64 transformation_finished_at_unix_nanos
uint64 processing_finished_at_unix_nanos
byte[payload_length] jpeg
```

Integers use big-endian byte order. Payloads must be 1 byte to 16 MiB. `NS`
means Unix nanoseconds. TCP reads can split or combine frames, so readers must
read the complete header and declared payload length. See
[framing.go](benchmark/framing/framing.go) for the implementation. Synchronize
host clocks when measuring end-to-end latency across machines.

## More Docs

- [Writing and deploying a function](examples/README.md).
- [Running benchmarks across two VMs](benchmark/README.md), with scripts that
  store each run under `results/<run-id>/`.
- `make help` lists the workflow commands. The [Makefile](Makefile) contains
  their configurable defaults.
