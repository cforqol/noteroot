package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/state"
	"noteroot/internal/version"
)

// diagnosticsReport is the JSON manifest bundled as diagnostics.json
// inside the zip diagnostics() returns.
type diagnosticsReport struct {
	GeneratedAt time.Time               `json:"generatedAt"`
	Version     string                  `json:"version"`
	GoVersion   string                  `json:"goVersion"`
	OS          string                  `json:"os"`
	Arch        string                  `json:"arch"`
	Goroutines  int                     `json:"goroutines"`
	Notebook    notebookDiagnostics     `json:"notebook"`
	GlobalQuota *globalQuotaDiagnostics `json:"globalQuota,omitempty"`
}

type notebookDiagnostics struct {
	Slug          string                 `json:"slug"`
	Name          string                 `json:"name"`
	Profile       string                 `json:"profile"`
	QuotaBytes    int64                  `json:"quotaBytes"`
	UsedBytes     int64                  `json:"usedBytes"`
	ReservedBytes int64                  `json:"reservedBytes"`
	VaultRowCount int                    `json:"vaultRowCount"`
	Databases     []dbStat               `json:"databases"`
	Breaker       notebooks.BreakerState `json:"breaker"`
}

type dbStat struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Bytes  int64  `json:"bytes"`
}

type globalQuotaDiagnostics struct {
	QuotaBytes    int64 `json:"quotaBytes"`
	UsedBytes     int64 `json:"usedBytes"`
	ReservedBytes int64 `json:"reservedBytes"`
}

// diagnostics serves POST /n/{slug}/diagnostics: a zip bundling
// version info, this notebook's DB file sizes and vault row count, its
// circuit breaker state (#44), the global quota snapshot when enabled
// (#47), recent Warn/Error log lines, and a full goroutine stack dump
// — one artifact to attach when reporting a problem, without shell
// access to the host.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	report := s.buildDiagnosticsReport(r.Context(), nb)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-diagnostics.zip"`, safeFilename(slug)))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	if f, err := zw.Create("diagnostics.json"); err == nil {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			s.log.Warn("diagnostics: encode failed", "notebook", slug, "err", err)
		}
	}
	if f, err := zw.Create("goroutines.txt"); err == nil {
		if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
			s.log.Warn("diagnostics: goroutine dump failed", "notebook", slug, "err", err)
		}
	}
	if f, err := zw.Create("recent-errors.log"); err == nil {
		for _, e := range s.recentLogs.Snapshot() {
			if _, err := fmt.Fprintf(f, "%s %s %s\n", e.Time.Format(time.RFC3339), e.Level, e.Message); err != nil {
				s.log.Warn("diagnostics: recent-errors.log write failed", "notebook", slug, "err", err)
				break
			}
		}
	}
}

// buildDiagnosticsReport collects everything diagnostics.json needs.
// A notebook whose state DB can't be opened right now still gets a
// report — just without the row count/usage fields a healthy DB would
// add — rather than failing the whole bundle: the bundle exists
// precisely to help diagnose a notebook that might be in trouble.
func (s *Server) buildDiagnosticsReport(ctx context.Context, nb *notebooks.Notebook) diagnosticsReport {
	report := diagnosticsReport{
		GeneratedAt: time.Now().UTC(),
		Version:     version.V,
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Goroutines:  runtime.NumGoroutine(),
		Notebook: notebookDiagnostics{
			Slug:       nb.Manifest.Slug,
			Name:       nb.Manifest.Name,
			Profile:    nb.Manifest.Profile,
			QuotaBytes: s.cfg.QuotaBytes,
			Databases: []dbStat{
				dbFileStat("state.db", nb.Layout.StateDB()),
				dbFileStat("index.db", nb.Layout.IndexDB()),
				dbFileStat("platform.db", nb.Layout.PlatformDB()),
			},
			Breaker: s.breakers.State(nb.Manifest.Slug),
		},
	}

	if db, _, err := state.Open(ctx, nb.Layout.StateDB()); err != nil {
		s.log.Warn("diagnostics: state open failed", "notebook", nb.Manifest.Slug, "err", err)
	} else {
		defer func() { _ = db.Close() }()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM note_identity`).Scan(&count); err != nil {
			s.log.Warn("diagnostics: vault row count failed", "notebook", nb.Manifest.Slug, "err", err)
		} else {
			report.Notebook.VaultRowCount = count
		}
		if used, reserved, err := resources.NewJournal(db.DB).QuotaSnapshot(ctx, s.quotaCutoff()); err != nil {
			s.log.Warn("diagnostics: quota snapshot failed", "notebook", nb.Manifest.Slug, "err", err)
		} else {
			report.Notebook.UsedBytes = used
			report.Notebook.ReservedBytes = reserved
		}
	}

	if s.cfg.GlobalQuotaBytes > 0 {
		used, reserved := s.globalUsedAndReserved(ctx)
		report.GlobalQuota = &globalQuotaDiagnostics{
			QuotaBytes:    s.cfg.GlobalQuotaBytes,
			UsedBytes:     used,
			ReservedBytes: reserved,
		}
	}
	return report
}

func dbFileStat(name, path string) dbStat {
	st := dbStat{Name: name, Path: path}
	if fi, err := os.Stat(path); err == nil {
		st.Exists = true
		st.Bytes = fi.Size()
	}
	return st
}

// adminHealthRow is one notebook's line in the /admin/health table.
type adminHealthRow struct {
	Slug, Name                           string
	BreakerOpen                          bool
	Strikes, Trips                       int
	LastError                            string
	UsedBytes, ReservedBytes, QuotaBytes int64
}

// adminHealth serves GET /admin/health: a plain, server-rendered page
// summarising every notebook's circuit breaker state (#44) and
// resource usage against its own quota, plus the global quota (#47)
// when one is configured — a single glance across every notebook
// without needing a diagnostics bundle per notebook.
func (s *Server) adminHealth(w http.ResponseWriter, r *http.Request) {
	rows := s.adminHealthRows(r.Context())

	var globalLine string
	if s.cfg.GlobalQuotaBytes > 0 {
		used, reserved := s.globalUsedAndReserved(r.Context())
		globalLine = fmt.Sprintf(
			`<p class="global">Global quota: %d used + %d reserved of %d bytes</p>`,
			used, reserved, s.cfg.GlobalQuotaBytes)
	}

	var rowsHTML strings.Builder
	for _, row := range rows {
		status := "closed"
		statusClass := "ok"
		if row.BreakerOpen {
			status = "open"
			statusClass = "bad"
		}
		quota := "unlimited"
		if row.QuotaBytes > 0 {
			quota = fmt.Sprintf("%d", row.QuotaBytes)
		}
		fmt.Fprintf(&rowsHTML,
			`<tr><td>%s</td><td>%s</td><td class="%s">%s</td><td>%d</td><td>%d</td>`+
				`<td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>`+"\n",
			html.EscapeString(row.Slug), html.EscapeString(row.Name),
			statusClass, status, row.Strikes, row.Trips,
			quota, row.UsedBytes, row.ReservedBytes, html.EscapeString(row.LastError))
	}

	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>noteroot health</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.5; max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
table { border-collapse: collapse; width: 100%%; margin: 1rem 0; }
th, td { border: 1px solid #ddd; padding: 0.5rem 0.75rem; text-align: left; }
th { background: #f4f4f4; }
.ok { color: #157a2e; font-weight: 600; }
.bad { color: #a21c1c; font-weight: 600; }
.global { color: #444; }
</style>
</head>
<body>
<h1>noteroot health</h1>
<p>version %s</p>
%s
<table>
<thead><tr><th>slug</th><th>name</th><th>breaker</th><th>strikes</th><th>trips</th>
<th>quota bytes</th><th>used</th><th>reserved</th><th>last breaker error</th></tr></thead>
<tbody>
%s</tbody>
</table>
</body>
</html>`, html.EscapeString(version.V), globalLine, rowsHTML.String())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

func (s *Server) adminHealthRows(ctx context.Context) []adminHealthRow {
	rows := make([]adminHealthRow, 0, len(s.reg.List()))
	for _, nb := range s.reg.List() {
		br := s.breakers.State(nb.Manifest.Slug)
		row := adminHealthRow{
			Slug: nb.Manifest.Slug, Name: nb.Manifest.Name,
			BreakerOpen: br.Open, Strikes: br.Strikes, Trips: br.Trips, LastError: br.LastError,
			QuotaBytes: s.cfg.QuotaBytes,
		}
		if db, _, err := state.Open(ctx, nb.Layout.StateDB()); err == nil {
			if used, reserved, err := resources.NewJournal(db.DB).QuotaSnapshot(ctx, s.quotaCutoff()); err == nil {
				row.UsedBytes, row.ReservedBytes = used, reserved
			}
			_ = db.Close()
		} else {
			s.log.Warn("admin health: state open failed", "notebook", nb.Manifest.Slug, "err", err)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slug < rows[j].Slug })
	return rows
}
