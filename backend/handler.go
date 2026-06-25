package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// indexHandler renders a simple landing page with a URL bar so users can type
// a destination site and enter the proxy.
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

// browseHandler is the core proxy endpoint. It expects ?url=<target> and
// streams back the rewritten HTML so all subsequent clicks stay proxied.
func browseHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing 'url' query parameter", http.StatusBadRequest)
		return
	}

	// Allow users to type bare domains like "example.com".
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "https://" + target
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}

	body, err := proxyPage(target)
	if err != nil {
		log.Printf("proxy error for %s: %v", target, err)
		http.Error(w, "failed to fetch page: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, body)
}
