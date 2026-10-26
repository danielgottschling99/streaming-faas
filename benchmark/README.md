# Two-VM benchmark scripts

These scripts assume the repository is available on both VMs. The client VM
runs them. The SUT VM only needs the FaaS server and Docker.

Create the client configuration:

```bash
cp benchmark/gcp.env.example benchmark/gcp.env
# Edit benchmark/gcp.env with the private VM IPs.
```

Check connectivity:

```bash
scripts/benchmark/preflight.sh
```

Deploy from the client:

```bash
scripts/benchmark/deploy.sh --mode benchmark
```

Available modes are `demo`, `benchmark`, `spatial-demo`, and
`spatial-benchmark`.

The runner supports independent roles:

```bash
scripts/benchmark/run.sh --mode benchmark --role sink --frames 30
scripts/benchmark/run.sh --mode benchmark --role source --frames 30
```

The source role starts the source, waits for its local listener, and performs
the `connect` call. If you want completely manual control, use the Makefile
targets directly and run `make connect` separately.

For convenience, `--role all` starts the sink and then runs the source/connect
side in one client process:

```bash
scripts/benchmark/run.sh --mode spatial-benchmark --role all --frames 30 --run-id run-001
```

Benchmark metrics are written below `results/<run-id>/`. Demo modes use the
ffplay sink and do not produce sink metrics.

For benchmark runs, `results/<run-id>/activation-metrics.jsonl` records the
attachment request. The activation-to-first-result interval is that timestamp
to `first_output_frame_at` in `sink-metrics.jsonl`. Both timestamps should be
read from hosts with synchronized clocks in a distributed run.
