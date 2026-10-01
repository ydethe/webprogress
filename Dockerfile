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

# --- wget stage ---
# busybox is statically linked, so its wget applet drops cleanly into the
# distroless (libc-less) runtime. Expose it as a plain `wget` command.
FROM busybox:1.37.0-musl AS wget
RUN mkdir -p /stage/bin \
 && cp /bin/busybox /stage/bin/busybox \
 && ln -s busybox /stage/bin/wget

# --- runtime stage ---
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/webprogress /webprogress
# Static wget (via busybox), so a wget-based healthcheck works on the distroless base.
COPY --from=wget /stage/bin/ /usr/bin/

# The server listens on 8775 (see internal/server.Port).
EXPOSE 8775

# OIDC and session configuration is supplied at runtime via WEBPROGRESS_* env vars.
ENTRYPOINT ["/webprogress"]
