package components

import (
	"bytes"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/opts"
)

// SourceQualityChart renders a simple bar chart of quality scores per source.
func SourceQualityChart(snapshots []models.DataSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{Title: "Source Data Quality"}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
	)

	var xAxis []string
	var data []opts.BarData

	for _, s := range snapshots {
		xAxis = append(xAxis, s.SourceCode)
		data = append(data, opts.BarData{Value: s.QualityScore * 100})
	}

	bar.SetXAxis(xAxis).
		AddSeries("Quality %", data)

	var buf bytes.Buffer
	_ = bar.Render(&buf)
	return buf.String()
}
