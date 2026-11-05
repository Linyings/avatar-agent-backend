package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestOpenAIAccountFailurePreservesSafeCode(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"code":"1113","message":"余额不足 secret-api-key"}}`)
			}))
			defer s.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: s.URL, OpenAIProModel: "test"}, nil)
			var err error
			if stream {
				err = client.Stream(context.Background(), "script", map[string]any{}, func(string, map[string]any) error { t.Error("unexpected completion"); return nil })
			} else {
				_, err = client.completeNonStream(context.Background(), client.profileFor("script", nil), map[string]any{})
			}
			var diagnostic *providerHTTPError
			if !errors.As(err, &diagnostic) || diagnostic.status != 429 || diagnostic.ProviderDiagnosticFields()["provider_code"] != "1113" {
				t.Fatalf("diagnostic = %v", err)
			}
			if strings.Contains(fmt.Sprint(err, diagnostic.ProviderDiagnosticFields()), "secret-api-key") {
				t.Fatal("upstream body leaked")
			}
			if err.Error() != "模型调用失败，请联系客服" {
				t.Fatalf("legacy endpoint message = %v", err)
			}
		})
	}
}

func TestOpenAIHTTPErrorRejectsArbitraryDiagnosticCodes(t *testing.T) {
	err := newOpenAIHTTPError(429, []byte(`{"error":{"code":"secret-key","type":"secret-type","message":"secret-body"}}`))
	if strings.Contains(fmt.Sprintf("%v %v", err, err.(*providerHTTPError).ProviderDiagnosticFields()), "secret") {
		t.Fatal("untrusted diagnostics leaked")
	}
}
