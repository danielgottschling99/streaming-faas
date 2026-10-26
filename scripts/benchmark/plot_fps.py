#!/usr/bin/env python3
"""Plot effective FPS for repeated benchmark runs.

Example:
  python3 scripts/benchmark/plot_fps.py \
    --run 1:gcp-w1-001 --run 1:gcp-w1-002 --run 1:gcp-w1-003 \
    --run 2:gcp-w2-001 --run 2:gcp-w2-002 --run 2:gcp-w2-003 \
    --run 4:gcp-w4-001 --run 4:gcp-w4-002 --run 4:gcp-w4-003 \
    --output results/fps-by-workers.png
"""

import argparse
import json
from collections import defaultdict
from pathlib import Path


def read_fps(metrics_file: Path) -> float:
    lines = [line for line in metrics_file.read_text().splitlines() if line.strip()]
    if not lines:
        raise ValueError("metrics file is empty")
    metric = json.loads(lines[-1])
    if "effective_fps" not in metric:
        raise ValueError("metric does not contain effective_fps")
    return float(metric["effective_fps"])


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--run",
        action="append",
        required=True,
        metavar="WORKERS:RUN_ID",
        help="benchmark run mapping; repeat once per run",
    )
    parser.add_argument(
        "--results-dir",
        default="results",
        type=Path,
        help="directory containing run result directories (default: results)",
    )
    parser.add_argument(
        "--output",
        default="results/fps-by-workers.png",
        type=Path,
        help="output image path",
    )
    args = parser.parse_args()

    values = defaultdict(list)
    labels = []
    for item in args.run:
        try:
            workers_text, run_id = item.split(":", 1)
            workers = int(workers_text)
        except ValueError as exc:
            raise SystemExit(f"invalid --run {item!r}; expected WORKERS:RUN_ID") from exc
        if workers < 1 or not run_id:
            raise SystemExit(f"invalid --run {item!r}")
        metrics_file = args.results_dir / run_id / "sink-metrics.jsonl"
        try:
            fps = read_fps(metrics_file)
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            raise SystemExit(f"could not read {metrics_file}: {exc}") from exc
        values[workers].append(fps)
        labels.append((workers, run_id, fps))

    try:
        import matplotlib.pyplot as plt
    except ImportError as exc:
        raise SystemExit("matplotlib is required: python3 -m pip install matplotlib") from exc

    workers = sorted(values)
    fig, ax = plt.subplots(figsize=(8, 5))
    for index, worker_count in enumerate(workers):
        measurements = values[worker_count]
        jitter = [index + (i - (len(measurements) - 1) / 2) * 0.06 for i in range(len(measurements))]
        ax.scatter(jitter, measurements, color="#3274a1", alpha=0.85, zorder=3)
        mean = sum(measurements) / len(measurements)
        ax.scatter(index, mean, color="#d62728", marker="D", zorder=4)

    means = [sum(values[count]) / len(values[count]) for count in workers]
    ax.plot(range(len(workers)), means, color="#d62728", label="mean", zorder=2)
    ax.set_xticks(range(len(workers)), [str(count) for count in workers])
    ax.set_xlabel("Number of workers")
    ax.set_ylabel("Effective FPS")
    ax.set_title("Segmentation benchmark throughput")
    ax.grid(axis="y", alpha=0.3)
    ax.legend()
    fig.tight_layout()

    args.output.parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(args.output, dpi=160)
    print(f"wrote {args.output}")
    for worker_count in workers:
        print(f"{worker_count} workers: {', '.join(f'{fps:.3f}' for fps in values[worker_count])} FPS")


if __name__ == "__main__":
    main()
