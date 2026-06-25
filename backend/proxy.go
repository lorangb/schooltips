package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ── helpers ────────────────────────────────────────────────────────────────

// isHTML checks if the response has an HTML content type.
func isHTML(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml")
}

// isRedirect returns true for 3xx status codes.
func isRedirect(code int) bool {
	return code >= 300 && code < 400
}

// resolveURL turns a possibly-relative href into an absolute URL using base.
func resolveURL(base *url.URL, href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(u).String()
}

// proxiedURL converts an absolute URL into a /browse?url=... link.
func proxiedURL(raw string) string {
	return "/browse?url=" + url.QueryEscape(raw)
}

// ── request forwarding ─────────────────────────────────────────────────────

// proxyRequest forwards the incoming request (method, headers, body) to target.
// It does NOT follow redirects — returns the raw 3xx response so we can rewrite
// the Location header.
func proxyRequest(r *http.Request, target string) (*http.Response, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	proxyReq, err := http.NewRequest(r.Method, target, r.Body)
	if err != nil {
		return nil, err
	}

	// Copy relevant request headers, skipping hop-by-hop headers.
	// Also skip Accept-Encoding — Go's http.Client auto-negotiates gzip/deflate
	// but does NOT support brotli. If we forward the client's Accept-Encoding
	// (which may include 'br'), the upstream may return brotli and we'd serve
	// compressed garbage as HTML.
	skipHeaders := map[string]bool{
		"host":              true,
		"connection":        true,
		"proxy-connection":  true,
		"upgrade":           true,
		"transfer-encoding": true,
		"accept-encoding":   true,
		"sec-fetch-site":    true,
		"sec-fetch-mode":    true,
		"sec-fetch-dest":    true,
		"sec-fetch-user":    true,
	}
	for key, vals := range r.Header {
		if skipHeaders[strings.ToLower(key)] {
			continue
		}
		for _, v := range vals {
			proxyReq.Header.Add(key, v)
		}
	}

	// Override UA to look like a real browser.
	proxyReq.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	proxyReq.Header.Set("Accept",
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	proxyReq.Host = targetURL.Host

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // we handle redirects ourselves
		},
	}
	return client.Do(proxyReq)
}

// ── attribute rewriting ────────────────────────────────────────────────────

// rewriteAttrs is the set of HTML attributes whose values are URLs that we need
// to route back through the proxy.
var rewriteAttrs = map[string]bool{
	"href":       true,
	"src":        true,
	"action":     true,
	"srcset":     true,
	"data-src":   true,
	"data-href":  true,
	"data-url":   true,
	"poster":     true,
	"cite":       true,
	"formaction": true,
	"data":       true,
	"background": true,
	"longdesc":   true,
	"profile":    true,
	"usemap":     true,
	"classid":    true,
	"codebase":   true,
	"code":       true,
	"archive":    true,
}

// removeAttrs are HTML attributes that break proxied content and must be stripped.
var removeAttrs = map[string]bool{
	"integrity": true,
}

// ── HTML rewriting ─────────────────────────────────────────────────────────

// rewriteHTML walks the parsed HTML tree and rewrites every relevant attribute
// so navigation stays inside the proxy. It also strips integrity attributes.
func rewriteHTML(n *html.Node, base *url.URL) {
	if n.Type == html.ElementNode {
		// Remove problematic attributes
		for i := len(n.Attr) - 1; i >= 0; i-- {
			if removeAttrs[n.Attr[i].Key] {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			}
		}

		// Rewrite URL attributes
		for i, a := range n.Attr {
			if !rewriteAttrs[a.Key] {
				continue
			}
			if a.Val == "" {
				continue
			}
			if strings.HasPrefix(a.Val, "#") || strings.HasPrefix(a.Val, "javascript:") {
				continue
			}
			abs := resolveURL(base, a.Val)
			if abs == "" {
				continue
			}
			if a.Key == "srcset" {
				n.Attr[i].Val = rewriteSrcset(abs, base)
			} else if a.Key == "data" {
				// <object data> might point to non-HTML resources
				n.Attr[i].Val = proxiedURL(abs)
			} else {
				n.Attr[i].Val = proxiedURL(abs)
			}
		}

		// Handle <meta http-equiv="refresh" content="0; url=...">
		if n.Data == "meta" {
			var isRefresh bool
			var contentIdx int
			contentVal := ""
			for i, a := range n.Attr {
				if a.Key == "http-equiv" && strings.ToLower(a.Val) == "refresh" {
					isRefresh = true
				}
				if a.Key == "content" {
					contentIdx = i
					contentVal = a.Val
				}
			}
			if isRefresh && contentVal != "" {
				re := regexp.MustCompile(`url=([^\s;]+)`)
				if m := re.FindStringSubmatch(contentVal); len(m) > 1 {
					abs := resolveURL(base, m[1])
					n.Attr[contentIdx].Val = re.ReplaceAllString(contentVal, "url="+proxiedURL(abs))
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteHTML(c, base)
	}
}

// rewriteSrcset handles srcset values which are comma-separated "URL descriptor" pairs.
func rewriteSrcset(val string, base *url.URL) string {
	parts := strings.Split(val, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		abs := resolveURL(base, fields[0])
		fields[0] = proxiedURL(abs)
		parts[i] = strings.Join(fields, " ")
	}
	return strings.Join(parts, ", ")
}

// injectBaseTag adds or updates a <base> tag in <head> with href pointing to
// the proxy, and target="_self" so all relative URLs auto-resolve through us.
func injectBaseTag(n *html.Node, pageURL string) {
	proxyBase := "/browse?url=" + url.QueryEscape(pageURL)

	// Walk the tree to find <head>.
	var head *html.Node
	var findHead func(*html.Node)
	findHead = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "head" {
			head = node
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			findHead(c)
		}
	}
	findHead(n)
	if head == nil {
		return
	}

	// Look for existing <base> and update its href.
	for c := head.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "base" {
			hasHref := false
			for i, a := range c.Attr {
				if a.Key == "href" {
					c.Attr[i].Val = proxyBase
					hasHref = true
				}
			}
			if !hasHref {
				c.Attr = append(c.Attr, html.Attribute{Key: "href", Val: proxyBase})
			}
			return
		}
	}

	// No existing — inject one at the beginning of <head>.
	baseNode := &html.Node{
		Type: html.ElementNode,
		Data: "base",
		Attr: []html.Attribute{
			{Key: "href", Val: proxyBase},
			{Key: "target", Val: "_self"},
		},
	}
	head.InsertBefore(baseNode, head.FirstChild)
}

// ── JavaScript interceptor ─────────────────────────────────────────────────

// proxyInterceptorScript returns a <script> tag that intercepts fetch/XHR/
// EventSource/history at runtime and routes all URLs through the proxy.
//
// Only proxies absolute (http/https) and protocol-relative (//) URLs.
// Relative URLs are handled automatically by the <base> tag at the browser
// level — the browser resolves them against the proxy base before sending
// the request.
func proxyInterceptorScript() string {
	return `<script data-st="1">
(function(){
var P='/browse?url=';
function p(u){
if(!u||u.indexOf('data:')===0||u.indexOf('blob:')===0||u.indexOf('javascript:')===0||u.indexOf('#')===0||u.indexOf(P)===0)return u;
if(u.indexOf('//')===0){u='https:'+u};
if(u.indexOf('http')===0){return P+encodeURIComponent(u)};
return u;
}
var f=window.fetch;window.fetch=function(u,o){return f.call(window,p(u),o)};
var o=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,u){return o.call(this,m,p(u))};
var E=window.EventSource;if(E){var W=window.EventSource;window.EventSource=function(u,c){return new W(p(u),c)};window.EventSource.prototype=W.prototype}
var h=history.pushState;history.pushState=function(s,t,u){return h.call(this,s,t,u?p(u):u)};
var r=history.replaceState;history.replaceState=function(s,t,u){return r.call(this,s,t,u?p(u):u)};
})();
</script>`
}

// ── CSS URL rewriting ──────────────────────────────────────────────────────

var (
	cssURLRe    = regexp.MustCompile(`url\((['"]?)([^'"()\s]+)(['"]?)\)`)
	cssImportRe = regexp.MustCompile(`@import\s+(?:url\(['"]?([^'"()\s]+)['"]?\)|['"]([^'"]+)['"])`)
)

// rewriteCSSURLs rewrites all url() references and @imports in CSS content so
// they route through the proxy.
func rewriteCSSURLs(css string, targetURL string) string {
	base, err := url.Parse(targetURL)
	if err != nil {
		return css
	}

	// Rewrite url(...) references.
	css = cssURLRe.ReplaceAllStringFunc(css, func(match string) string {
		parts := cssURLRe.FindStringSubmatch(match)
		if len(parts) < 4 || parts[2] == "" {
			return match
		}
		abs := resolveURL(base, parts[2])
		return "url(" + parts[1] + proxiedURL(abs) + parts[3] + ")"
	})

	// Rewrite @import without url() wrapper — @import "path".
	css = cssImportRe.ReplaceAllStringFunc(css, func(match string) string {
		parts := cssImportRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		path := parts[1]
		if path == "" {
			path = parts[2]
		}
		if path == "" {
			return match
		}
		abs := resolveURL(base, path)
		return "@import url(" + proxiedURL(abs) + ")"
	})

	return css
}

// ── legacy helpers (kept for compatibility) ────────────────────────────────

// fetchURL performs an HTTP GET against target with a browser-like UA.
func fetchURL(target string) (*http.Response, error) {
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept",
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	client := &http.Client{}
	return client.Do(req)
}

// proxyRaw streams the upstream response body directly to the writer with
// original Content-Type. Used for CSS, JS, images, etc.
func proxyRaw(w http.ResponseWriter, target string) error {
	resp, err := fetchURL(target)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	if resp.Header.Get("Cache-Control") != "" {
		w.Header().Set("Cache-Control", resp.Header.Get("Cache-Control"))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// proxyPage fetches HTML, rewrites it, and returns as string.
func proxyPage(target string) (string, error) {
	resp, err := fetchURL(target)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	base, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	rewriteHTML(doc, base)
	injectBaseTag(doc, target)

	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return "", fmt.Errorf("render html: %w", err)
	}

	result := b.String()
	result = strings.Replace(result, "</body>", proxyInterceptorScript()+"</body>", 1)
	return result, nil
}