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

// forwardedResponseHeaders are copied from upstream to the proxy response
// for all content types (HTML, CSS, JS, images, etc.).
var forwardedResponseHeaders = map[string]bool{
	"content-type":                     true,
	"content-length":                   true,
	"cache-control":                    true,
	"expires":                          true,
	"etag":                             true,
	"last-modified":                    true,
	"set-cookie":                       true,
	"access-control-allow-origin":      true,
	"access-control-expose-headers":    true,
	"access-control-allow-credentials": true,
	"access-control-allow-methods":     true,
	"access-control-allow-headers":     true,
	"content-security-policy":          true,
	"strict-transport-security":        true,
	"x-frame-options":                  true,
	"x-content-type-options":           true,
	"x-xss-protection":                 true,
}

// forwardHeaders copies allowed upstream response headers to the proxy response.
func forwardHeaders(w http.ResponseWriter, resp *http.Response) {
	for key, vals := range resp.Header {
		if forwardedResponseHeaders[strings.ToLower(key)] {
			for _, v := range vals {
				w.Header().Add(key, v)
			}
		}
	}
}

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
//   - Rewrites redirect Location headers back through the proxy, preserving status
//   - Rewrites CSS url() and @import references for proxied CSS files
//   - Parses and rewrites all HTML links/attributes so navigation stays proxied
//   - Injects a <base> tag so relative URLs auto-resolve through the proxy
//   - Injects a JS interceptor that patches fetch/XHR/history at runtime
//   - Passes non-HTML content (images, JS, fonts) through raw
//   - Forwards critical response headers (CORS, Set-Cookie, caching, security)
//   - Forwards browser cookies to upstream for session support
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
	// Rewrite the Location header so the browser stays inside the proxy,
	// preserving the original status code (301, 302, 307, 308).
	if isRedirect(resp.StatusCode) {
		location := resp.Header.Get("Location")
		if location == "" {
			http.Error(w, "redirect with no location", resp.StatusCode)
			return
		}
		absLoc := resolveURL(parsedTarget, location)
		http.Redirect(w, r, proxiedURL(absLoc), resp.StatusCode)
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
		forwardHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(rewriteCSSURLs(string(body), target)))
		return
	}

	// ── Non-HTML: raw passthrough ────────────────────────────────────────
	if !isHTML(resp) {
		forwardHeaders(w, resp)
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
		forwardHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		return
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		// If parsing fails, serve raw HTML so page isn't blank.
		log.Printf("html parse error for %s: %v", target, err)
		forwardHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	rewriteHTML(doc, parsedTarget)
	injectBaseTag(doc, target)

	var buf strings.Builder
	if err := html.Render(&buf, doc); err != nil {
		log.Printf("html render error: %v", err)
		forwardHeaders(w, resp)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	result := buf.String()

	// Inject the JS proxy interceptor right before </body>.
	result = strings.Replace(result, "</body>", proxyInterceptorScript()+"</body>", 1)

	forwardHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	w.Write([]byte(result))
}