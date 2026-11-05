package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

func TestAvatarContentNetworkDiagnosticNeverIncludesRawEndpoint(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&net.DNSError{Err: "private-dns-details", Name: "private-host"}, "network_dns"},
		{context.DeadlineExceeded, "network_timeout"},
		{io.ErrUnexpectedEOF, "connection_interrupted"},
		{errors.New("private-unknown-error"), "request_failed"},
	} {
		d := (&contentRequestDiagnostic{phase: "request_sent"}).wrap(tc.err).(*contentDiagnosticError)
		if d.code != tc.code || strings.Contains(d.Error(), "private") || !errors.Is(d, tc.err) {
			t.Fatalf("incorrect safe classification: %v", d)
		}
	}
}

func TestAvatarContentFailureRecordsSafeResponseDiagnostic(t *testing.T) {
	for _, tc := range []struct{ body, code string }{{"private-invalid-response", "response_invalid_json"}, {`{"choices":[]}`, "response_no_choices"}, {`{"choices":[{"message":{"content":"not-json-private"}}]}`, "model_invalid_json"}} {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "req-diagnostic")
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "private-key", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "test"}, nil)
			path := filepath.Join(t.TempDir(), "operations.jsonl")
			ctx, _ := avatar.BeginOperation(avatar.WithTelemetry(context.Background(), path), "F03", "")
			_, err := client.AvatarContent(ctx, "private-script", map[string]string{"script": "private-script"}, nil)
			if err == nil {
				t.Fatal("expected failed content response")
			}
			data, _ := os.ReadFile(path)
			var event map[string]any
			if json.Unmarshal(data, &event) != nil || event["error_code"] != tc.code || event["http_status"] != float64(200) || event["status"] != "failed" {
				t.Fatalf("missing safe failure evidence: %s", data)
			}
			for _, secret := range []string{"private-key", "private-script", tc.body, srv.URL} {
				if strings.Contains(string(data), secret) {
					t.Fatal("sensitive diagnostics persisted")
				}
			}
		})
	}
}
