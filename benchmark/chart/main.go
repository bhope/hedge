package main

import (
	"image/color"
	"log"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

type dataset struct {
	label  string
	color  color.RGBA
	values plotter.Values
}

var (
	groups = []string{"p50", "p90", "p95", "p99", "p999"}

	data = []dataset{
		{
			label:  "No hedging",
			color:  color.RGBA{R: 0xB4, G: 0xB2, B: 0xA9, A: 0xFF},
			values: plotter.Values{5.1, 9.0, 18.8, 65.0, 103.8},
		},
		{
			label:  "Static 10ms",
			color:  color.RGBA{R: 0x85, G: 0xB7, B: 0xEB, A: 0xFF},
			values: plotter.Values{5.0, 9.0, 13.3, 17.5, 61.2},
		},
		{
			label:  "Static 50ms",
			color:  color.RGBA{R: 0x37, G: 0x8A, B: 0xDD, A: 0xFF},
			values: plotter.Values{5.0, 9.0, 16.5, 54.9, 59.7},
		},
		{
			label:  "Adaptive (hedge)",
			color:  color.RGBA{R: 0x5D, G: 0xCA, B: 0xA5, A: 0xFF},
			values: plotter.Values{5.0, 8.9, 12.3, 17.3, 63.5},
		},
	}
)

func main() {
	p := plot.New()
	p.Title.Text = "Tail latency: 50,000 requests with 5% stragglers"
	p.Title.Padding = vg.Points(10)
	p.Y.Label.Text = "Latency (ms)"
	p.BackgroundColor = color.White

	const (
		barWidth = 18.0
		barGap   = 4.0
		step     = barWidth + barGap
	)

	n := float64(len(data))
	for i, d := range data {
		bars, err := plotter.NewBarChart(d.values, vg.Points(barWidth))
		if err != nil {
			log.Fatal(err)
		}
		bars.Color = d.color
		bars.LineStyle.Width = 0
		bars.Offset = vg.Points((float64(i) - (n-1)/2) * step)

		p.Add(bars)
		p.Legend.Add(d.label, bars)
	}

	p.NominalX(groups...)
	p.X.Min = -0.5
	p.X.Max = float64(len(groups)) - 0.5
	p.Y.Min = 0

	p.Legend.Top = true
	p.Legend.Left = false

	if err := p.Save(8*vg.Inch, 4*vg.Inch, "../../eval.png"); err != nil {
		log.Fatal(err)
	}
}
