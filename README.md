# SchoolTips

A lightweight web proxy for school iPads. Bypasses MDM domain blocks by routing all traffic through one custom domain.

## How It Works

Kid goes to `schooltips.io` → types URL → server fetches the page and rewrites all links to go through the proxy. The school's MDM only sees `schooltips.io`.

```
Kid's iPad ──▶ schooltips.io ──▶ Server fetches page ──▶ Rewritten HTML
                                   │                     (all links → /browse?url=)
                                   ▼
                              Any website
```

## Architecture

- **Proxy server:** Go binary (~7MB, single file, no dependencies)
- **Frontend:** GitHub Pages (landing page with URL input)
- **Deployment:** Docker or bare binary on any Linux host

## Quick Start

```bash
cd backend
./schooltips
# Listening on :8080
# Open http://localhost:8080 in browser
```

## Deploy

### Using Docker
```bash
cd backend
docker build -t schooltips .
docker run -d -p 8080:8080 schooltips
```

### Using bare binary
```bash
# Build from source
cd backend
CGO_ENABLED=0 go build -o schooltips .

# Run
./schooltips
```

### Point a domain at it
1. Point `schooltips.io` DNS A record → your server IP
2. Put Caddy or Nginx in front for HTTPS (Let's Encrypt auto)
3. That's it

## Files

```
schooltips/
├── backend/
│   ├── main.go        # Entry point, routes
│   ├── proxy.go       # URL fetch + HTML rewriting
│   ├── handler.go     # HTTP handlers
│   ├── go.mod         # Go module
│   ├── Dockerfile     # Multi-stage Docker build
│   └── schooltips     # Pre-built binary (Linux amd64)
├── frontend/
│   ├── index.html     # Landing page
│   ├── css/style.css
│   └── js/script.js
└── README.md
```

## Building from Source

Requires Go 1.21+.

```bash
cd backend
go mod tidy
CGO_ENABLED=0 go build -o schooltips .
```
