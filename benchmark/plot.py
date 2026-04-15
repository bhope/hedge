import matplotlib.pyplot as plt

# --- eval.png: tail latency for 50,000 requests with 5% stragglers ---
# Data from benchmark/results.csv
labels = ["p50", "p90", "p95", "p99", "p999"]
x = range(len(labels))

data = {
    "No hedging":       [5.0,  8.9, 17.1, 64.3, 104.5],
    "Static 10ms":      [5.0,  8.9, 13.1, 17.4,  46.5],
    "Static 50ms":      [5.0,  8.9, 18.9, 54.7,  60.2],
    "Adaptive (hedge)": [5.0,  8.9, 12.3, 17.0,  59.4],
}

colors = {
    "No hedging":       "tab:blue",
    "Static 10ms":      "tab:orange",
    "Static 50ms":      "tab:green",
    "Adaptive (hedge)": "tab:red",
}

fig, ax = plt.subplots(figsize=(10, 4))

for label, values in data.items():
    ax.plot(x, values, marker="o", label=label, color=colors[label])

ax.set_xticks(list(x))
ax.set_xticklabels(labels)
ax.set_ylabel("Latency (ms)")
ax.set_title("Tail latency: 50,000 requests with 5% stragglers")
ax.legend(loc="upper left")
ax.grid(True, color="lightgray")
ax.set_facecolor("white")
fig.patch.set_facecolor("white")

fig.tight_layout()
fig.savefig("../eval.png", dpi=150)
print("Saved eval.png")

# --- eval_streaming.png: sketch signal comparison ---
# Data from benchmark/streaming/results.csv
# Three lines: No hedging (blue), TTFB-miscalibrated (gray dashed), TTFT-calibrated (red)
stream_labels = ["p50", "p90", "p95", "p99"]
sx = range(len(stream_labels))

# No hedging baseline
no_hedge_values = [16.4, 199.6, 217.5, 245.6]
# TTFB-miscalibrated: hedges on time-to-headers (broken — hedges ~100%, massive waste)
ttfb_values = [14.7, 19.4, 23.6, 198.7]
# TTFT-calibrated: hedges on time-to-first-body-byte (fixed — hedges only slow outliers)
ttft_values = [16.4, 61.6, 182.5, 224.2]

fig2, ax2 = plt.subplots(figsize=(10, 4))

ax2.plot(sx, no_hedge_values, marker="o", label="No hedging", color="tab:blue")
ax2.plot(sx, ttfb_values, marker="o", label="TTFB-miscalibrated", color="gray", linestyle="--")
ax2.plot(sx, ttft_values, marker="o", label="TTFT-calibrated", color="tab:red")

ax2.set_xticks(list(sx))
ax2.set_xticklabels(stream_labels)
ax2.set_ylabel("Latency (ms)")
ax2.set_title("End-to-end latency: streaming server")
ax2.legend(loc="upper left")
ax2.grid(True, color="lightgray")
ax2.set_facecolor("white")
fig2.patch.set_facecolor("white")

fig2.tight_layout()
fig2.savefig("../eval_streaming.png", dpi=150)
print("Saved eval_streaming.png")
