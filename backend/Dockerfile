# ---- build stage ----
FROM golang:1.21-alpine AS builder

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/schooltips .

# ---- runtime stage ----
FROM scratch

COPY --from=builder /out/schooltips /schooltips

EXPOSE 8080

ENTRYPOINT ["/schooltips"]
