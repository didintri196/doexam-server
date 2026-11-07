"""Build the LOADTEST.md charts from the k6 summaries, API request logs and resource samples.

python3 -I charts.py <outdir> <runs.json>
runs.json: [{"name": "run2", "label": "500", "participants": 500, "summary": path, "resources": path,
             "apilog": path, "start": "HH:MM:SS", "end": "HH:MM:SS", "after_fix": true}, ...]
"""
import json
import sys
from collections import defaultdict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

# reference palette (dataviz skill, light mode); slots in fixed order
SURFACE, INK, INK2, MUTED, GRID, AXIS = "#fcfcfb", "#0b0b0b", "#52514e", "#898781", "#e1e0d9", "#c3c2b7"
SLOTS = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4"]

plt.rcParams.update({
    "figure.facecolor": SURFACE, "axes.facecolor": SURFACE, "savefig.facecolor": SURFACE,
    "axes.edgecolor": AXIS, "axes.labelcolor": INK2, "xtick.color": MUTED, "ytick.color": MUTED,
    "text.color": INK, "font.size": 10, "axes.titlesize": 12, "axes.titleweight": "semibold",
    "axes.titlelocation": "left", "axes.spines.top": False, "axes.spines.right": False,
    "axes.grid": True, "grid.color": GRID, "grid.linewidth": 0.6, "axes.axisbelow": True,
    "legend.frameon": False, "lines.linewidth": 2,
})

OUT, RUNS = sys.argv[1], json.load(open(sys.argv[2]))


def metric(summary, name, stat):
    return json.load(open(summary))["metrics"][name][stat]


# ── 1. latency (p95) per participant count, one line per path ────────────────
def latency_by_scale():
    runs = [r for r in RUNS if r["after_fix"]]
    xs = [r["participants"] for r in runs]
    series = [("Heartbeat (sync kosong)", "lt_sync_heartbeat"), ("Kirim jawaban", "lt_sync_answer"),
              ("Mulai ujian", "lt_start"), ("Login", "lt_login"), ("Monitor pengawas", "lt_monitor")]
    fig, ax = plt.subplots(figsize=(8, 5))
    for i, (label, m) in enumerate(series):
        ys = [metric(r["summary"], m, "p(95)") for r in runs]
        ax.plot(xs, ys, color=SLOTS[i], marker="o", markersize=7, markeredgecolor=SURFACE, markeredgewidth=2)
        pass  # 5 series: the legend names them (no direct labels, they collide at the right edge)
    ax.set_xticks(xs, [f"{x:,}".replace(",", ".") for x in xs])
    ax.set_xlabel("Peserta serentak")
    ax.set_ylabel("p95 latensi (ms, skala log)")
    ax.set_yscale("log")
    ax.axhline(300, color=AXIS, linewidth=1, linestyle=(0, (4, 3)))
    ax.annotate("ambang heartbeat 300 ms", (xs[0], 300), xytext=(0, 4), textcoords="offset points", color=MUTED, fontsize=8)
    ax.set_xlim(xs[0] - 200, xs[-1] + 200)
    ax.set_title("Latensi p95 per jalur, menurut jumlah peserta")
    for i, (label, _) in enumerate(series):
        ax.plot([], [], color=SLOTS[i], label=label)
    ax.legend(loc="upper center", bbox_to_anchor=(0.4, -0.18), fontsize=9, ncol=3)
    fig.tight_layout()
    fig.savefig(f"{OUT}/latency-by-scale.png", dpi=160)
    plt.close(fig)


# ── 2. monitor before/after the lock fix (same 500 participants) ─────────────
def monitor_fix():
    before = next(r for r in RUNS if not r["after_fix"])
    after = next(r for r in RUNS if r["after_fix"] and r["participants"] == before["participants"])
    b = [metric(before["summary"], "lt_monitor", "p(95)"),
         max(metric(before["summary"], "lt_sync_heartbeat", "max"), metric(before["summary"], "lt_sync_answer", "max"))]
    a = [metric(after["summary"], "lt_monitor", "p(95)"), None]
    fig, ax = plt.subplots(figsize=(7.4, 2.4))
    rows = [("Sebelum perbaikan (run 1)", b[0], SLOTS[1]), ("Sesudah perbaikan (run 2)", a[0], SLOTS[0])]
    for i, (label, v, c) in enumerate(rows):
        ax.barh([1 - i], [v], height=0.38, color=c)
        ax.annotate(f"{v:.0f} ms", (v, 1 - i), xytext=(6, 0), textcoords="offset points", va="center", color=INK2)
    ax.set_yticks([1, 0], [r[0] for r in rows])
    ax.set_xlabel("ms")
    ax.set_xlim(0, b[0] * 1.3)
    ax.grid(axis="y", visible=False)
    ax.set_title("Monitor pengawas p95 (500 peserta)")
    fig.tight_layout()
    fig.savefig(f"{OUT}/monitor-fix.png", dpi=160)
    plt.close(fig)


# ── 3. timeline of the peak run: throughput, sync p95, CPU (small multiples) ──
def parse_api(path, start, end):
    """Per-10s buckets of request count and sync durations, from the JSON request log."""
    count, sync = defaultdict(int), defaultdict(list)
    for line in open(path):
        if not line.startswith("{"):
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if "duration" not in d or "path" not in d:
            continue
        t = d["time"][11:19]
        if not (start <= t <= end):
            continue
        h, m, s = map(int, t.split(":"))
        b = (h * 3600 + m * 60 + s) // 10 * 10
        count[b] += 1
        if d["path"].endswith("/events"):
            sync[b].append(d["duration"] / 1e6)
    return count, sync


def secs(t):
    h, m, s = map(int, t.split(":"))
    return h * 3600 + m * 60 + s


def timeline(run):
    count, sync = parse_api(run["apilog"], run["start"], run["end"])
    t0 = secs(run["start"])
    xs = sorted(count)
    rps = [count[b] / 10 for b in xs]
    p95 = []
    for b in xs:
        v = sorted(sync[b])
        p95.append(v[int(len(v) * 0.95)] if v else None)
    cpu_t, cpu = [], []
    for line in open(run["resources"]):
        p = line.split()
        t = secs(p[0])
        if secs(run["start"]) <= t <= secs(run["end"]):
            cpu_t.append((t - t0) / 60)
            cpu.append(float(p[2]))
    mins = [(b - t0) / 60 for b in xs]
    fig, axes = plt.subplots(3, 1, figsize=(8, 7.2), sharex=True)
    axes[0].plot(mins, rps, color=SLOTS[0])
    axes[0].set_ylabel("request/detik")
    axes[0].set_title(f"Satu run {run['label']} peserta: beban, latensi, CPU (per 10 detik)")
    pts = [(m, v) for m, v in zip(mins, p95) if v is not None]
    axes[1].plot([m for m, _ in pts], [v for _, v in pts], color=SLOTS[0])
    axes[1].set_ylabel("p95 sync (ms, log)")
    axes[1].set_yscale("log")
    axes[2].plot(cpu_t, cpu, color=SLOTS[0])
    axes[2].axhline(200, color=AXIS, linewidth=1, linestyle=(0, (4, 3)))
    axes[2].annotate("batas 2 CPU (200%)", (cpu_t[0] if cpu_t else 0, 200), xytext=(0, -12), textcoords="offset points",
                     color=MUTED, fontsize=8)
    axes[2].set_ylabel("CPU API (%)")
    axes[2].set_ylim(0, 220)
    axes[2].set_xlabel("menit sejak sesi dibuka")
    for ax in axes:
        if ax is not axes[1]:
            ax.set_ylim(bottom=0)
        ax.axvspan(0, 1, color=GRID, alpha=0.45, linewidth=0)
    axes[0].annotate("gelombang login", (0.5, max(rps) * 0.92), ha="center", color=MUTED, fontsize=8)
    fig.tight_layout()
    fig.savefig(f"{OUT}/timeline-{run['name']}.png", dpi=160)
    plt.close(fig)
    return {"rps_max": max(rps), "p95_max": max(v for _, v in pts), "cpu_max": max(cpu) if cpu else None}


latency_by_scale()
monitor_fix()
for r in RUNS:
    if r["after_fix"] and r["participants"] >= 1000:
        print(r["name"], json.dumps(timeline(r)))
