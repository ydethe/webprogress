// Command webprogress runs the webprogress server: a token-authenticated update
// ingest endpoint plus an OIDC-guarded live dashboard, on port 8775.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/ydethe/webprogress/internal/server"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /health endpoint and exit 0 (ok) or 1 (fail)")
	noauth := flag.Bool("noauth", false, "disable authentication entirely (testing only): no OIDC login, and ingest needs no token")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	if err := server.Run(*noauth); err != nil {
		log.Fatalf("webprogress: %v", err)
	}
}

// runHealthcheck lets the container healthcheck probe liveness without curl, so
// the image can stay on a minimal distroless base.
func runHealthcheck() int {
	resp, err := http.Get("http://127.0.0.1:" + server.Port + "/health")
	if err != nil {
		log.Printf("healthcheck: %v", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthcheck: status %d", resp.StatusCode)
		return 1
	}
	return 0
}
