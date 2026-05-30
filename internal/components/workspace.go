package components

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/bmcelhaney/insight-forge/internal/models"
	g "github.com/maragudk/gomponents"
	. "github.com/maragudk/gomponents/html"
	ds "maragu.dev/gomponents-datastar"
)

// WorkspaceProps holds everything needed to render the full reactive workspace.
type WorkspaceProps struct {
	NSN               string
	Result            *models.InsightResult
	Snapshots         []models.DataSnapshot
	RecentNSNs        []string
	IsAnalyzing       bool
	CompletedSources  []string // for live progress
	TotalSources      int
	ErrorMessage      string
	BasePath          string // e.g. "/insightforge" — must start with /
}

// Workspace is the main reactive component rendered via Datastar.
func Workspace(props WorkspaceProps) g.Node {
	return Div(
		Class("grid grid-cols-1 xl:grid-cols-12 gap-6"),
		// Optional subpath banner (helpful when running under /insightforge)
		g.If(props.BasePath != "",
			Div(Class("xl:col-span-12 mb-2"),
				Div(Class("alert alert-info py-2 text-sm"),
					g.Text("Running under subpath: "),
					Span(Class("font-mono font-bold"), g.Text(props.BasePath)),
				),
			),
		),
		// Left sidebar: Search + Recent
		Div(Class("xl:col-span-3"),
			SearchPanel(props),
			RecentPanel(props),
		),
		// Center: Data + Visualizations
		Div(Class("xl:col-span-6"),
			MainDataPanel(props),
		),
		// Right: Insight Card
		Div(Class("xl:col-span-3"),
			InsightCard(props),
		),
	)
}

func SearchPanel(props WorkspaceProps) g.Node {
	return Div(Class("card bg-base-100 shadow-xl mb-4"),
		Div(Class("card-body"),
			H3(Class("card-title text-lg"), g.Text("NSN Analysis")),
			Div(Class("flex gap-2"),
				Input(
					Type("text"),
					Class("input input-bordered flex-1 font-mono"),
					Placeholder("NSN or NIIN"),
					Value(props.NSN),
					ds.Model("nsn"),
				),
				Button(
					Class("btn btn-primary"),
					ds.On("click", fmt.Sprintf(`@post('%s', {nsn: $nsn})`, props.path("/datastar/analyze"))),
					g.Text("Analyze"),
				),
			),
			P(Class("text-xs opacity-60 mt-1"), g.Text("Multi-source extraction + synthesis")),
		),
	)
}

func RecentPanel(props WorkspaceProps) g.Node {
	return Div(Class("card bg-base-100 shadow"),
		Div(Class("card-body"),
			H3(Class("card-title text-sm"), g.Text("Recent Analyses")),
			g.If(len(props.RecentNSNs) == 0,
				P(Class("text-sm opacity-60"), g.Text("No recent analyses yet.")),
			),
			g.Group(g.Map(props.RecentNSNs, func(nsn string) g.Node {
				return Button(
					Class("btn btn-sm btn-ghost justify-start w-full font-mono text-left mb-1"),
					ds.On("click", fmt.Sprintf(`@set($nsn, '%s'); @post('%s', {nsn: $nsn})`, nsn, props.path("/datastar/analyze"))),
					g.Text(nsn),
				)
			})),
		),
	)
}

func MainDataPanel(props WorkspaceProps) g.Node {
	if props.Result == nil && !props.IsAnalyzing {
		return Div(Class("card bg-base-100 shadow-xl h-96 flex items-center justify-center"),
			Div(Class("text-center"),
				H3(Class("text-xl opacity-70"), g.Text("Enter an NSN and click Analyze")),
				P(Class("opacity-50"), g.Text("Parallel extraction from WebFLIS, FPDS, Sanctions & more")),
			),
		)
	}

	if props.IsAnalyzing {
		progressText := "Gathering intelligence from sources..."
		if len(props.CompletedSources) > 0 && props.TotalSources > 0 {
			progressText = fmt.Sprintf("Completed %d/%d sources", len(props.CompletedSources), props.TotalSources)
		}
		return Div(Class("card bg-base-100 shadow-xl"),
			Div(Class("card-body"),
				Progress(Class("progress progress-primary w-full")),
				P(Class("text-center mt-3 font-medium"), g.Text(progressText)),
				g.If(len(props.CompletedSources) > 0,
					P(Class("text-center text-xs opacity-70 mt-1"), g.Text(strings.Join(props.CompletedSources, " → "))),
				),
				P(Class("text-center text-xs opacity-50 mt-2"), g.Text("Live updates as each source completes")),
			),
		)
	}

	return Div(Class("card bg-base-100 shadow-xl"),
		Div(Class("card-body"),
			H3(Class("card-title"), g.Text("Source Data & Trends")),
			P(Class("text-sm"), g.Textf("%d snapshots captured across sources", len(props.Snapshots))),

			// Source quality visualization (go-echarts)
			Div(Class("mt-4"),
				g.Raw(SourceQualityChart(props.Snapshots)),
			),

			// Source breakdown table
			Table(Class("table table-sm mt-4"),
				Thead(Tr(Th(g.Text("Source")), Th(g.Text("Quality")), Th(g.Text("Captured")))),
				Tbody(
					g.Group(g.Map(props.Snapshots, func(s models.DataSnapshot) g.Node {
						return Tr(
							Td(Class("font-mono"), g.Text(s.SourceCode)),
							Td(g.Textf("%.0f", s.QualityScore*100)),
							Td(g.Text(s.SnapshotAt.Format("2006-01-02"))),
						)
					})),
				),
			),

			Div(Class("mt-6 grid grid-cols-1 md:grid-cols-2 gap-6"),
				Div(
					H4(Class("font-semibold mb-2"), g.Text("Demand Signals")),
					g.Raw(DemandSignalsChart(props.Result)),
				),
				Div(
					H4(Class("font-semibold mb-2"), g.Text("Risk Factors")),
					g.Raw(RiskBreakdownChart(props.Result)),
				),
			),
		),
	)
}

func InsightCard(props WorkspaceProps) g.Node {
	if props.Result == nil {
		return Div()
	}

	r := props.Result

	return Div(Class("card bg-base-100 shadow-2xl border border-base-300"),
		Div(Class("card-body"),
			H3(Class("card-title"), g.Text("Insight Card")),

			Div(Class("stats stats-vertical shadow w-full mt-2"),
				Div(Class("stat"),
					Div(Class("stat-title"), g.Text("Viability Score")),
					Div(Class("stat-value text-success"), g.Textf("%.0f", r.ViabilityScore)),
				),
				Div(Class("stat"),
					Div(Class("stat-title"), g.Text("Risk Score")),
					Div(Class("stat-value text-error"), g.Textf("%.0f", r.RiskScore)),
				),
			),

			Div(Class("mt-4"),
				H4(Class("font-semibold"), g.Text("Executive Summary")),
				P(Class("text-sm mt-1"), g.Text(r.Summary)),
			),

			g.If(len(r.Flags) > 0,
				Div(Class("mt-4"),
					H4(Class("font-semibold mb-1"), g.Text("Flags")),
					g.Group(g.Map(r.Flags, func(f models.RiskFlag) g.Node {
						severityClass := "badge-warning"
						if f.Severity == "critical" {
							severityClass = "badge-error"
						}
						return Div(Class("badge "+severityClass+" mr-1 mb-1"), g.Text(f.Description))
					})),
				),
			),

			Div(Class("card-actions justify-end mt-6 gap-2"),
				Button(
					Class("btn btn-primary btn-sm"),
					ds.On("click", fmt.Sprintf(`window.location = '%s'`, props.path(fmt.Sprintf("/api/export/%s", r.EntityID)))),
					g.Text("Export JSON"),
				),
				Button(
					Class("btn btn-accent btn-sm"),
					ds.On("click", fmt.Sprintf(`window.location = '%s'`, props.path(fmt.Sprintf("/api/export-excel/%s", r.EntityID)))),
					g.Text("Export Excel Bundle"),
				),
				Button(
					Class("btn btn-ghost btn-sm"),
					ds.On("click", fmt.Sprintf(`@set($nsn, '%s'); @post('%s', {nsn: $nsn})`, r.EntityID, props.path("/datastar/analyze"))),
					g.Text("Re-run"),
				),
			),
		),
	)
}

// RenderWorkspaceToString is a helper for initial page load and SSE patches.
func RenderWorkspaceToString(props WorkspaceProps) (string, error) {
	var buf bytes.Buffer
	err := Workspace(props).Render(&buf)
	return buf.String(), err
}

// path joins the BasePath with a route.
func (p WorkspaceProps) path(route string) string {
	if p.BasePath == "" {
		return route
	}
	if route == "" || route == "/" {
		return p.BasePath
	}
	if route[0] != '/' {
		route = "/" + route
	}
	return p.BasePath + route
}
