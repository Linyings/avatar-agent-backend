package production

import (
	"os"
	"strings"
	"testing"
)

func TestProductionIngressAndPortPolicy(t *testing.T) {
	composeBytes, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	caddyBytes, err := os.ReadFile("Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	compose, caddy := string(composeBytes), string(caddyBytes)
	for _, required := range []string{"FRONTEND_IMAGE", "BACKEND_IMAGE", "127.0.0.1:15432:5432", "caddy_data", "postgres_data", "ALIPAY_CERT_HOST_DIR", "/app/data/alipay_certs:ro"} {
		if !strings.Contains(compose, required) {
			t.Fatalf("compose missing %q", required)
		}
	}
	if strings.Contains(compose, `"8000:8000"`) {
		t.Fatal("API port must not be public")
	}
	for _, required := range []string{"narratoai.cn", "handle /api/health", "rewrite * /health", "handle /api/fish-audio-proxy/*", "handle /api/*", "reverse_proxy api:8000", "reverse_proxy frontend:80"} {
		if !strings.Contains(caddy, required) {
			t.Fatalf("Caddyfile missing %q", required)
		}
	}
}
