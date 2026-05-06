package api

/*

  — All REST HTTP Handlers

  ROUTES (all require JWT via Authorization: Bearer):
  GET  /api/docs             → list user's docs
  POST /api/docs             → create new doc
  GET  /api/docs/:id         → doc + online users
  GET  /api/docs/:id/tasks   → all tasks for doc
  POST /api/docs/:id/tasks   → assign new task
  PUT  /api/docs/:id/tasks/:tid → complete/update task
  GET  /api/docs/:id/analytics → contribution stats
  POST /api/docs/:id/export  → PDF export
  GET  /api/health           → liveness probe

  PDF EXPORT LOGIC:
  1. Load doc metadata from DB
  2. Build HTML from a Go template (3 formats)
  3. Shell out to wkhtmltopdf (or chromedp)
  4. Stream PDF bytes with Content-Disposition header
  Fallback: return print-ready HTML if binary missing

*/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pixell07/equalink/analytics"
	"github.com/pixell07/equalink/auth"
	"github.com/pixell07/equalink/document"
	"github.com/pixell07/equalink/hub"
	appSync "github.com/pixell07/equalink/sync"
	"github.com/pixell07/equalink/task"
)

// Handlers bundles all dependencies for REST handlers.
type Handlers struct {
	Store       *document.Store
	Tracker     *analytics.Tracker
	Hub         *hub.Hub
	SyncHandler *appSync.Handler
	TaskService *task.Service
	WkhtmlPath  string // path to wkhtmltopdf binary
}

// Health

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status":      "ok",
		"connections": h.Hub.TotalConnections(),
		"time":        time.Now().UTC(),
	})
}

// Documents

// ListDocuments — GET /api/docs
func (h *Handlers) ListDocuments(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	docs, err := h.Store.ListDocuments(userID)
	if err != nil {
		httpError(w, "could not fetch documents", 500)
		return
	}
	writeJSON(w, map[string]any{"docs": docs})
}

// CreateDocument — POST /api/docs
func (h *Handlers) CreateDocument(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)

	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		httpError(w, "title is required", 400)
		return
	}

	doc, err := h.Store.CreateDocument(body.Title, userID)
	if err != nil {
		httpError(w, "could not create document", 500)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, doc)
}

// Tasks

// GetTasks — GET /api/docs/:id/tasks
func (h *Handlers) GetTasks(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	docID := r.PathValue("id")

	// Verify access
	if err := h.SyncHandler.Validator.CheckDocAccess(docID, userID); err != nil {
		httpError(w, "access denied", 403)
		return
	}

	tasks, err := h.Store.GetTasks(docID)
	if err != nil {
		httpError(w, "could not fetch tasks", 500)
		return
	}
	writeJSON(w, map[string]any{"tasks": tasks})
}

// CompleteTask — PUT /api/docs/:id/tasks/:tid
func (h *Handlers) CompleteTask(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	tidStr := r.PathValue("tid")
	tid, err := strconv.ParseUint(tidStr, 10, 64)
	if err != nil {
		httpError(w, "invalid task id", 400)
		return
	}

	if err := h.TaskService.CompleteTask(tid, userID); err != nil {
		httpError(w, err.Error(), 403)
		return
	}
	writeJSON(w, map[string]string{"status": "done"})
}

// Analytics

// GetAnalytics — GET /api/docs/:id/analytics
// Merges historical DB data with live in-memory tracker data.
func (h *Handlers) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	docID := r.PathValue("id")

	if err := h.SyncHandler.Validator.CheckDocAccess(docID, userID); err != nil {
		httpError(w, "access denied", 403)
		return
	}

	// Historical stats from DB
	historical, err := h.Store.GetContributions(docID)
	if err != nil {
		httpError(w, "could not fetch analytics", 500)
		return
	}

	// Live (unflushed) stats from RAM
	live := h.Tracker.GetSnapshot(docID)

	// Merge and compute percentages
	stats := analytics.MergeWithLive(historical, live)

	// Also get task accountability scores
	accountability, _ := h.TaskService.AccountabilityScore(docID)

	writeJSON(w, map[string]any{
		"contributions":  stats,
		"accountability": accountability,
		"online_count":   len(h.Hub.OnlineInDoc(docID)),
	})
}

// PDF Export

// ExportPDF — POST /api/docs/:id/export
// Body: { "format": "full" | "contributions" | "tasks" }
func (h *Handlers) ExportPDF(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(auth.UserIDKey).(string)
	docID := r.PathValue("id")

	var body struct {
		Format string `json:"format"` // "full" | "contributions" | "tasks"
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Format == "" {
		body.Format = "full"
	}

	// Load doc (also verifies access)
	doc, err := h.Store.LoadDocument(docID, userID)
	if err != nil {
		httpError(w, "document not found", 404)
		return
	}

	// Build HTML for the selected format
	var htmlContent string
	switch body.Format {
	case "contributions":
		historical, _ := h.Store.GetContributions(docID)
		live := h.Tracker.GetSnapshot(docID)
		stats := analytics.MergeWithLive(historical, live)
		htmlContent = buildContributionHTML(doc, stats)
	case "tasks":
		tasks, _ := h.Store.GetTasks(docID)
		htmlContent = buildTaskHTML(doc, tasks)
	default:
		htmlContent = buildFullDocHTML(doc)
	}

	// Convert HTML → PDF via wkhtmltopdf
	pdfBytes, err := htmlToPDF(htmlContent, h.WkhtmlPath)
	if err != nil {
		// Fallback: return print-ready HTML so browser Ctrl+P works
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlContent))
		return
	}

	filename := fmt.Sprintf("equalink-%s-%s.pdf", doc.Title, time.Now().Format("2006-01-02"))
	// Sanitize filename
	filename = strings.ReplaceAll(filename, " ", "-")

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(pdfBytes)))
	w.Write(pdfBytes)
}

// HTML Templates

var pdfBaseCSS = `
  @import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap');
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:'Inter',Arial,sans-serif;font-size:14px;line-height:1.7;color:#111;padding:48px 64px;max-width:860px;margin:0 auto}
  .header{border-bottom:2px solid #111;padding-bottom:16px;margin-bottom:36px}
  .brand{font-size:11px;font-weight:600;letter-spacing:.1em;text-transform:uppercase;color:#888;margin-bottom:6px}
  h1{font-size:28px;font-weight:700;line-height:1.3;margin-bottom:4px}
  .meta{font-size:12px;color:#999}
  h2{font-size:18px;font-weight:600;margin:28px 0 10px}
  p{margin-bottom:12px}
  ul,ol{padding-left:24px;margin-bottom:12px}
  blockquote{border-left:3px solid #333;padding-left:16px;color:#555;font-style:italic;margin:16px 0}
  table{width:100%;border-collapse:collapse;margin-top:8px}
  th{text-align:left;padding:8px 12px;font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:#666;border-bottom:2px solid #111}
  td{padding:10px 12px;border-bottom:1px solid #eee}
  .bar-wrap{background:#f0f0f0;border-radius:99px;height:7px;width:160px;overflow:hidden;display:inline-block;vertical-align:middle}
  .bar-fill{height:100%;border-radius:99px;background:#111}
  .badge{display:inline-block;padding:2px 8px;border-radius:99px;font-size:11px;font-weight:600}
  .done{background:#e8f5e9;color:#2e7d32}.open{background:#fff3e0;color:#e65100}
  .footer{border-top:1px solid #ddd;margin-top:48px;padding-top:12px;font-size:11px;color:#aaa;display:flex;justify-content:space-between}
  @page{margin:0}
  @media print{body{padding:32px 48px}}
`

func buildFullDocHTML(doc *document.Document) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="UTF-8">
<style>%s</style></head><body>
<div class="header">
  <div class="brand">EqualInk — Collaborative Document</div>
  <h1>%s</h1>
  <div class="meta">Exported %s</div>
</div>
<p><em>Document body — integrate Yjs-to-HTML renderer (y-prosemirror) to decode the CRDT blob.</em></p>
<div class="footer"><span>EqualInk · equalink.app</span><span>CRDT-powered collaboration</span></div>
</body></html>`, pdfBaseCSS, doc.Title, time.Now().Format("Jan 2, 2006 15:04"))
}

func buildContributionHTML(doc *document.Document, stats []analytics.UserStat) string {
	var rows strings.Builder
	for _, s := range stats {
		rows.WriteString(fmt.Sprintf(
			`<tr><td>%s</td><td>%d</td><td>%s</td><td>%.1f%%</td>`+
				`<td><div class="bar-wrap"><div class="bar-fill" style="width:%.0f%%"></div></div></td></tr>`,
			s.Name, s.EditCount, formatBytes(s.BytesAdded), s.Percentage, s.Percentage,
		))
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="UTF-8">
<style>%s</style></head><body>
<div class="header">
  <div class="brand">EqualInk — Contribution Report</div>
  <h1>%s</h1>
  <div class="meta">Generated %s</div>
</div>
<table>
  <thead><tr><th>Member</th><th>Edits</th><th>Written</th><th>Share</th><th>Contribution</th></tr></thead>
  <tbody>%s</tbody>
</table>
<div class="footer"><span>EqualInk · equalink.app</span><span>Analytics powered by CRDT tracking</span></div>
</body></html>`, pdfBaseCSS, doc.Title, time.Now().Format("Jan 2, 2006"), rows.String())
}

func buildTaskHTML(doc *document.Document, tasks []document.Task) string {
	var rows strings.Builder
	for _, t := range tasks {
		assignee := "Unassigned"
		if t.Assignee != nil {
			assignee = t.Assignee.Name
		}
		badgeClass := t.Status
		rows.WriteString(fmt.Sprintf(
			`<tr><td>%s</td><td>%s</td><td><span class="badge %s">%s</span></td><td>%s</td></tr>`,
			t.Title, assignee, badgeClass, t.Status, t.CreatedAt.Format("Jan 2"),
		))
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="UTF-8">
<style>%s</style></head><body>
<div class="header">
  <div class="brand">EqualInk — Task Summary</div>
  <h1>%s</h1>
  <div class="meta">Generated %s · %d tasks total</div>
</div>
<table>
  <thead><tr><th>Task</th><th>Assigned to</th><th>Status</th><th>Created</th></tr></thead>
  <tbody>%s</tbody>
</table>
<div class="footer"><span>EqualInk · equalink.app</span></div>
</body></html>`, pdfBaseCSS, doc.Title, time.Now().Format("Jan 2, 2006"), len(tasks), rows.String())
}

// wkhtmltopdf integration

// htmlToPDF converts HTML to PDF bytes by shelling out to wkhtmltopdf.
// Install: apt-get install wkhtmltopdf  (Ubuntu/Debian)
//
//	brew install wkhtmltopdf     (macOS)
//
// In Docker: use surnet/alpine-wkhtmltopdf as base image.
func htmlToPDF(html, wkhtmlPath string) ([]byte, error) {
	if wkhtmlPath == "" {
		wkhtmlPath = "wkhtmltopdf"
	}

	// Write HTML to stdin, read PDF from stdout
	// Flags: --quiet (suppress progress), - - (stdin/stdout)
	cmd := exec.Command(wkhtmlPath,
		"--quiet",
		"--enable-local-file-access",
		"--margin-top", "0",
		"--margin-bottom", "0",
		"--margin-left", "0",
		"--margin-right", "0",
		"-", // read from stdin
		"-", // write to stdout
	)
	cmd.Stdin = strings.NewReader(html)

	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wkhtmltopdf: %w — stderr: %s", err, errBuf.String())
	}
	return out.Bytes(), nil
}

func formatBytes(b int) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	return fmt.Sprintf("%.1f KB", float64(b)/1024)
}
