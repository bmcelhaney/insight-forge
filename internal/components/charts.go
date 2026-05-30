package components

import (
	"bytes"

	"github.com/bmcelhaney/insight-forge/internal/models"
	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/opts"
)

// SourceQualityChart renders a bar chart of quality scores per source.
func SourceQualityChart(snapshots []models.DataSnapshot) string {
	if len(snapshots) == 0 {
		return "<div class='text-sm opacity-60'>No source data yet.</div>"
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{Title: "Source Data Quality (%)", Subtitle: "Higher = better evidence"}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
		charts.WithTooltipOpts(opts.Tooltip{Show: true}),
	)

	var xAxis []string
	var data []opts.BarData

	for _, s := range snapshots {
		xAxis = append(xAxis, s.SourceCode)
		data = append(data, opts.BarData{Value: s.QualityScore * 100})
	}

	bar.SetXAxis(xAxis).
		AddSeries("Quality", data).
		SetSeriesOptions(charts.WithLabelOpts(opts.Label{Show: true}))

	var buf bytes.Buffer
	_ = bar.Render(&buf)
	return buf.String()
}

// DemandSignalsChart shows a simple representation of demand (prototype values from synthesis).
func DemandSignalsChart(result *models.InsightResult) string {
	if result == nil {
		return ""
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{Title: "Demand Signals (Prototype)"}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
	)

	d := result.DemandSignals
	xAxis := []string{"Awards", "Value (M USD)", "Agencies"}
	data := []opts.BarData{
		{Value: d.TotalAwards},
		{Value: int(d.TotalValueUSD / 1_000_000)},
		{Value: len(d.TopAgencies)},
	}

	bar.SetXAxis(xAxis).
		AddSeries("Signals", data)

	var buf bytes.Buffer
	_ = bar.Render(&buf)
	return buf.String()
}

// RiskBreakdownChart creates a simple horizontal bar for risk factors (prototype).
func RiskBreakdownChart(result *models.InsightResult) string {
	if result == nil || len(result.Flags) == 0 {
		return ""
	}

	bar := charts.NewBar()
	bar.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{Title: "Risk Factors"}),
		charts.WithLegendOpts(opts.Legend{Show: false}),
	)

	var xAxis []string
	var data []opts.BarData

	severityScore := map[string]float64{
		"low": 20, "medium": 50, "high": 75, "critical": 95,
	}

	for _, f := range result.Flags {
		xAxis = append(xAxis, f.Type)
		score := severityScore[f.Severity]
		if score == 0 {
			score = 40
		}
		data = append(data, opts.BarData{Value: score})
	}

	bar.SetXAxis(xAxis).
		AddSeries("Risk Level", data)

	var buf bytes.Buffer
	_ = bar.Render(&buf)
	return buf.String()
}
