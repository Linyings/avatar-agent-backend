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

func TestOpenAIContentReviewFailureIsSafeAndDistinctFromBilling(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"code":"data_inspection_failed","type":"data_inspection_failed","message":"<400> InternalError.Algo.DataInspectionFailed: Input video data may contain inappropriate content. private-material"}}`)
			}))
			defer s.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: s.URL, OpenAIProModel: "test"}, nil)
			var err error
			if stream {
				err = client.Stream(context.Background(), "script", map[string]any{}, func(string, map[string]any) error { t.Fatal("unexpected completion"); return nil })
			} else {
				_, err = client.completeNonStream(context.Background(), client.profileFor("script", nil), map[string]any{})
			}
			var diagnostic *providerHTTPError
			if !errors.As(err, &diagnostic) || diagnostic.status != 400 || diagnostic.ProviderDiagnosticFields()["provider_code"] != "content_blocked" || err.Error() != ProviderContentReviewMessage {
				t.Fatalf("unexpected failure: %v", err)
			}
			if strings.Contains(fmt.Sprint(err, diagnostic.ProviderDiagnosticFields()), "private-material") {
				t.Fatal("upstream content leaked")
			}
		})
	}
}

func TestOpenAIContentReviewCodesOnly(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"DataInspectionFailed"}}`,
		`{"error":{"type":"data_inspection_failed"}}`,
		`{"error":{"code":"InputDataInspectionFailed"}}`,
		`{"error":{"code":"OutputDataInspectionFailed"}}`,
	} {
		err := newOpenAIHTTPError(400, []byte(body)).(*providerHTTPError)
		if err.ProviderDiagnosticFields()["provider_code"] != "content_blocked" {
			t.Fatalf("not recognized: %s", body)
		}
	}
	for _, body := range []string{`{"error":{"code":"invalid_parameter","message":"bad video"}}`, `{"error":{"code":"secret-key"}}`, `not json`} {
		err := newOpenAIHTTPError(400, []byte(body)).(*providerHTTPError)
		if err.ProviderDiagnosticFields()["provider_code"] != "" || err.Error() == ProviderContentReviewMessage {
			t.Fatalf("misclassified: %s", body)
		}
	}
}
