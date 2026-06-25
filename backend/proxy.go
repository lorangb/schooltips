package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// fetchURL performs an HTTP GET against the target URL with a browser-like
// User-Agent so school filters that block default Go client UA still serve
// real content.
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

// isHTML checks if the response has an HTML content type.
func isHTML(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml+xml")
}

// proxyRaw streams the upstream response body directly to the provided writer
// with the original Content-Type header. Used for CSS, JS, images, etc.
func proxyRaw(w http.ResponseWriter, target string) error {
	resp, err := fetchURL(target)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	// Pass through Content-Type
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))

	// Copy any other useful headers
	if resp.Header.Get("Cache-Control") != "" {
		w.Header().Set("Cache-Control", resp.Header.Get("Cache-Control"))
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

// resolveURL turns a possibly-relative href found in the HTML into an absolute
// URL using the page's base URL.
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

// proxiedURL converts an absolute URL into a /browse?url=... link so that
// clicking the rewritten link re-enters the proxy.
func proxiedURL(raw string) string {
	return "/browse?url=" + url.QueryEscape(raw)
}

// rewriteAttrs is the set of HTML attributes whose values are URLs that we
// need to route back through the proxy.
var rewriteAttrs = map[string]bool{
	"href":   true,
	"src":    true,
	"action": true,
	"srcset": true,
}

// rewriteHTML walks the parsed HTML tree and rewrites every relevant
// attribute on every node so navigation stays inside the proxy.
func rewriteHTML(n *html.Node, base *url.URL) {
	if n.Type == html.ElementNode {
		for i, a := range n.Attr {
			if !rewriteAttrs[a.Key] {
				continue
			}
			abs := resolveURL(base, a.Val)
			if abs == "" || strings.HasPrefix(abs, "#") || strings.HasPrefix(abs, "javascript:") {
				continue
			}
			if a.Key == "srcset" {
				n.Attr[i].Val = rewriteSrcset(abs, base)
			} else {
				n.Attr[i].Val = proxiedURL(abs)
			}
		}
	}
	// Inject a <base> target so form posts and any missed links open in same tab.
	if n.Type == html.ElementNode && n.Data == "head" {
		baseNode := &html.Node{
			Type: html.ElementNode,
			Data: "base",
			Attr: []html.Attribute{{Key: "target", Val: "_self"}},
		}
		n.AppendChild(baseNode)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteHTML(c, base)
	}
}

// rewriteSrcset handles srcset values which are comma-separated
// "URL descriptor" pairs.
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

// proxyPage fetches the target URL, parses and rewrites the HTML, and returns
// the rewritten document as bytes.
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

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	rewriteHTML(doc, base)

	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return "", fmt.Errorf("render html: %w", err)
	}
	return b.String(), nil
}
