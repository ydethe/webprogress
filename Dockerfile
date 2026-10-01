# Image that runs the webprogress server (Go, on port 8775).
# The client is the Python tqdm library used elsewhere; it is not run from this image.

# --- build stage ---
FROM golang:1.26 AS build
WORKDIR /src

# Cache module downloads independently of the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Pure-Go build (modernc SQLite) → a static binary, so the final image needs no libc.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/webprogress ./cmd/webprogress

# --- runtime stage ---
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/webprogress /webprogress

# The server listens on 8775 (see internal/server.Port).
EXPOSE 8775

# Probe /health via the binary's own flag — no curl needed on a distroless base.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/webprogress", "--healthcheck"]

# OIDC and session configuration is supplied at runtime via WEBPROGRESS_* env vars.
ENTRYPOINT ["/webprogress"]
