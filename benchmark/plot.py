import matplotlib.pyplot as plt

labels = ["p50", "p90", "p95", "p99", "p999"]
x = range(len(labels))

data = {
    "No hedging":       [5.1,  9.0, 18.8, 65.0, 103.8],
    "Static 10ms":      [5.0,  9.0, 13.3, 17.5,  61.2],
    "Static 50ms":      [5.0,  9.0, 16.5, 54.9,  59.7],
    "Adaptive (hedge)": [5.0,  8.9, 12.3, 17.3,  63.5],
}

fig, ax = plt.subplots(figsize=(10, 4))

for label, values in data.items():
    ax.plot(x, values, marker="o", label=label)

ax.set_xticks(list(x))
ax.set_xticklabels(labels)
ax.set_ylabel("Latency (ms)")
ax.set_title("Tail latency: 50,000 requests with 5% stragglers")
ax.legend(loc="upper left")
ax.grid(True, color="lightgray")
ax.set_facecolor("white")
fig.patch.set_facecolor("white")

fig.tight_layout()
fig.savefig("eval.png", dpi=150)
