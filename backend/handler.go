package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// indexHandler renders the landing page with a URL bar.
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>SchoolTips Proxy</title></head>
<body style="font-family:system-ui;max-width:640px;margin:4rem auto;padding:0 1rem">
  <h1>SchoolTips Proxy</h1>
  <p>Enter a URL to browse through the proxy:</p>
  <form action="/browse" method="get">
    <input type="text" name="url" placeholder="https://example.com"
           style="width:70%;padding:0.5rem;font-size:1rem">
    <button type="submit" style="padding:0.5rem 1rem;font-size:1rem">Go</button>
  </form>
</body>
</html>`)
}

// browseHandler is the core proxy endpoint.
//
// It accepts any HTTP method (GET, POST, etc.), forwards the request to target,
// and:
//   - Rewrites redirect 3xx Location headers back through the proxy
//   - Rewrites CSS url() and @import references for proxied CSS files
//   - Parses and rewrites all HTML links/attributes so navigation stays proxied
//   - Injects a <base> tag so relative URLs auto-resolve through the proxy
//   - Injects a JS interceptor that patches fetch/XHR/history at runtime
//   - Passes non-HTML content (images, JS, fonts) through raw
func browseHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing 'url' query parameter", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "https://" + target
	}

	parsedTarget, err := url.Parse(target)
	if err != nil || parsedTarget.Host == "" {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}

	// Forward the request (GET, POST, etc.) to the target.
	resp, err := proxyRequest(r, target)
	if err != nil {
		log.Printf("proxy error for %s: %v", target, err)
		http.Error(w, "failed to fetch: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// ── Handle redirects ─────────────────────────────────────────────────
	// Rewrite the Location header so the browser stays inside the proxy.
	if isRedirect(resp.StatusCode) {
		location := resp.Header.Get("Location")
		if location == "" {
			http.Error(w, "redirect with no location", resp.StatusCode)
			return
		}
		absLoc := resolveURL(parsedTarget, location)
		http.Redirect(w, r, proxiedURL(absLoc), http.StatusFound)
		return
	}

	ct := resp.Header.Get("Content-Type")

	// ── CSS: rewrite url() and @import references ────────────────────────
	if strings.Contains(ct, "text/css") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read css", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if resp.Header.Get("Cache-Control") != "" {
			w.Header().Set("Cache-Control", resp.Header.Get("Cache-Control"))
		}
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(rewriteCSSURLs(string(body), target)))
		return
	}

	// ── Non-HTML: raw passthrough ────────────────────────────────────────
	if !isHTML(resp) {
		w.Header().Set("Content-Type", ct)
		if resp.Header.Get("Cache-Control") != "" {
			w.Header().Set("Cache-Control", resp.Header.Get("Cache-Control"))
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// ── HTML: parse, rewrite, inject ────────────────────────────────────
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadGateway)
		return
	}

	if len(body) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		return
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		// If parsing fails, serve raw HTML so page isn't blank.
		log.Printf("html parse error for %s: %v", target, err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	rewriteHTML(doc, parsedTarget)
	injectBaseTag(doc, target)

	var buf strings.Builder
	if err := html.Render(&buf, doc); err != nil {
		log.Printf("html render error: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	result := buf.String()

	// Inject the JS proxy interceptor right before </body>.
	result = strings.Replace(result, "</body>", proxyInterceptorScript()+"</body>", 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	w.Write([]byte(result))
}
