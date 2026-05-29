package server

import (
	"html/template"
	"net/http"
	"strings"
)

// errorPageTmpl renders a friendly standalone HTML page for a browser that
// lands on an unmatched backend route directly. The SPA (served by nginx)
// owns the in-app error experience; this is the fallback for a human who hits
// the API host with a browser. The palette mirrors the frontend editorial
// theme (cream + navy + amber) so the two surfaces feel like one product.
var errorPageTmpl = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Code}} — {{.Title}}</title>
<style>
*{box-sizing:border-box}
body{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fcf9ed;color:#000161}
.card{background:#fff;border:1px solid #e9e2d0;padding:2.75rem 3rem;max-width:480px;box-shadow:0 1px 2px rgba(0,1,97,.04)}
.code{font-size:.7rem;letter-spacing:.26em;text-transform:uppercase;color:#f5ac11;margin:0 0 .85rem}
h1{font-family:Georgia,'Times New Roman',serif;font-style:italic;font-weight:400;font-size:2rem;margin:0 0 1rem;line-height:1.1}
p.lede{color:#3d4775;font-size:.95rem;line-height:1.55;margin:0 0 1.5rem}
a{color:#0955d5;text-decoration:none;font-size:.8rem;letter-spacing:.06em}
a:hover{color:#0067fe}
</style>
</head>
<body>
<div class="card">
<p class="code">Error {{.Code}}</p>
<h1>{{.Title}}</h1>
<p class="lede">{{.Message}}</p>
<a href="/">← Back to the marketplace</a>
</div>
</body>
</html>`))

type errorPageData struct {
	Code    int
	Title   string
	Message string
}

// writeHTTPError responds with a content-negotiated error. A browser
// (Accept: text/html) gets the styled HTML page above; every other client —
// curl, the SPA's fetch calls, MCP tooling — gets the standard
// {"error":"..."} JSON used throughout the API. jsonMsg is the terse machine
// message; title/detail are the human-facing copy for the HTML page.
func writeHTTPError(w http.ResponseWriter, r *http.Request, status int, jsonMsg, title, detail string) {
	if acceptsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		errorPageTmpl.Execute(w, errorPageData{Code: status, Title: title, Message: detail})
		return
	}
	writeErr(w, status, jsonMsg)
}

// acceptsHTML reports whether the client prefers HTML. Browsers list
// "text/html" in Accept; API clients send application/json or */*.
func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// handleNotFound is chi's NotFound handler: it catches every route that no
// pattern matched (e.g. a mistyped /api path or a stray browser request).
func (a *App) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeHTTPError(w, r, http.StatusNotFound, "not found",
		"Page not found",
		"The page or resource you requested doesn't exist. It may have been moved or removed.")
}

// handleMethodNotAllowed is chi's MethodNotAllowed handler: the path matched a
// route but not for this HTTP method (e.g. POST to a GET-only endpoint).
func (a *App) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeHTTPError(w, r, http.StatusMethodNotAllowed, "method not allowed",
		"Method not allowed",
		"That action isn't supported for this URL.")
}
