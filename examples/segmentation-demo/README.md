# Live segmentation demonstration

This is the fast version of the segmentation experiment. It accepts the
repository's normalized framed-JPEG stream and emits a framed JPEG mask. Its
repeated refinement passes are kept for benchmarking.

Start the FaaS server first:

```bash
make start
```

Then use three terminals:

```bash
make deploy-segmentation-demo
make run-segmentation-demo-sink
make run-segmentation-demo-source
make connect FUNCTION_NAME=segmentation-demo CONNECT_PORT=9000
```

The sink defaults to 30 FPS. Change playback speed with, for example:

```bash
FRAME_PLAYBACK_FPS=10 make run-segmentation-demo-sink
```

To run the demonstration source at 8 FPS:

```bash
FRAME_FPS=8 make run-segmentation-demo-source
```

## Slow evaluation

The benchmark deployment enables the deliberately slow refinement passes and
records the output timing in the normal frame-sink metrics:

```bash
make deploy-segmentation-benchmark
make run-segmentation-evaluation-sink
make run-segmentation-evaluation
```

The last two commands use the configured `FRAME_SINK_METRICS` equivalent
`segmentation-sink-metrics.jsonl` and `FRAME_FRAMES` value. The combined
evaluation command assumes the FaaS server and sink are already running.
Change the amount of slow work with
`SEGMENTATION_BENCHMARK_PASSES=24 make deploy-segmentation-benchmark`.
For a quicker smoke test, use
`SEGMENTATION_BENCHMARK_INPUT=frame-input-low make run-segmentation-evaluation`.

## Spatially scaled evaluation

The existing framed-JPEG splitter and fuser can be used with segmentation
workers instead of the negate workers:

```bash
make deploy-segmentation-spatial-scale
make run-segmentation-spatial-evaluation-sink
make run-segmentation-spatial-evaluation
```

This deploys four workers by default and writes metrics to
`segmentation-spatial-sink-metrics.jsonl`. Change the worker count with a
positive power of two, for example
`SEGMENTATION_SCALE_WORKERS=8 make deploy-segmentation-spatial-scale`.

To save the received output frames as JPEGs, set `FRAME_SINK_SAVE_DIR` when
starting the sink. For example, this saves three frames under
`scaled-output/frame-1.jpg`, `frame-2.jpg`, and `frame-3.jpg`:

```bash
FRAME_FRAMES=3 FRAME_SINK_SAVE_DIR=scaled-output make run-segmentation-spatial-evaluation-sink
FRAME_FRAMES=3 make run-segmentation-spatial-evaluation
```

For the fast live demonstration with spatial scaling:

```bash
make deploy-segmentation-spatial-demo
make run-segmentation-demo-sink
make run-segmentation-spatial-demo
```

The demo uses the low-resolution frames by default and plays through the same
FPS-controlled `ffplay` sink as the single-worker demonstration.

The last two commands can be run in either order: the source waits for the
function connection, and the sink waits for the function output. The sink
passes each JPEG to `ffplay` as an MJPEG stream, so the binary human mask is
shown live.
