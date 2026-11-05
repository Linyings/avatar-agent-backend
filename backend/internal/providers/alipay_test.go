package providers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

func TestAlipayCreateSubscriptionSignsRequestAndVerifiesResponse(t *testing.T) {
	appPrivate, appPublic := generateAlipayTestKeyPair(t)
	alipayPrivate, alipayPublic := generateAlipayTestKeyPair(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("method"); got != "alipay.test.subscription.create" {
			t.Fatalf("unexpected method %q", got)
		}
		if err := verifyAlipayTestValues(r.Form, appPublic); err != nil {
			t.Fatalf("request signature verification failed: %v", err)
		}

		response := json.RawMessage(`{"code":"10000","msg":"Success","checkout_url":"https://cashier.example/checkout","request_id":"req_1"}`)
		signature := signAlipayTestBytes(t, alipayPrivate, response)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"alipay_test_subscription_create_response":`+string(response)+`,"sign":"`+signature+`"}`)
	}))
	defer server.Close()

	client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), server.Client(), AlipayMethods{
		CreateSubscription: "alipay.test.subscription.create",
	})
	checkout, err := client.CreateSubscription(context.Background(), AlipaySubscriptionRequest{
		RequestID: "req_1", PlanID: "plan_1", ProductID: "product_1", Amount: "29.90",
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.CheckoutURL != "https://cashier.example/checkout" || checkout.RequestID != "req_1" {
		t.Fatalf("unexpected checkout: %#v", checkout)
	}
}

func TestAlipayPagePaymentURLBuildsSignedCashierLink(t *testing.T) {
	appPrivate, appPublic := generateAlipayTestKeyPair(t)
	_, alipayPublic := generateAlipayTestKeyPair(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("page payment URL must not issue an HTTP request")
	}))
	defer server.Close()
	client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), server.Client(), AlipayMethods{})
	creditClient := client.(interface {
		PagePaymentURL(context.Context, AlipayPagePaymentRequest) (string, error)
	})
	raw, err := creditClient.PagePaymentURL(context.Background(), AlipayPagePaymentRequest{OutTradeNo: "CR1", Subject: "500 Credits", TotalAmount: "1.90"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme == "" || u.Host == "" {
		t.Fatalf("expected an absolute gateway URL, got %q", raw)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := query.Get("method"); got != "alipay.trade.page.pay" {
		t.Fatalf("method = %q", got)
	}
	if err := verifyAlipayTestValues(query, appPublic); err != nil {
		t.Fatalf("request signature verification failed: %v", err)
	}
	var biz map[string]any
	if err := json.Unmarshal([]byte(query.Get("biz_content")), &biz); err != nil {
		t.Fatal(err)
	}
	if biz["product_code"] != "FAST_INSTANT_TRADE_PAY" || biz["out_trade_no"] != "CR1" {
		t.Fatalf("unexpected biz_content: %v", biz)
	}
}

func TestAlipayPrecreateCreditPaymentUsesOrdinaryTradeMethod(t *testing.T) {
	appPrivate, appPublic := generateAlipayTestKeyPair(t)
	alipayPrivate, alipayPublic := generateAlipayTestKeyPair(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("method"); got != "alipay.trade.precreate" {
			t.Fatalf("method = %q", got)
		}
		if err := verifyAlipayTestValues(r.Form, appPublic); err != nil {
			t.Fatal(err)
		}
		var biz map[string]any
		if err := json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); err != nil {
			t.Fatal(err)
		}
		if biz["product_code"] != "QR_CODE_OFFLINE" || biz["out_trade_no"] != "CR1" {
			t.Fatalf("unexpected biz_content: %v", biz)
		}
		response := json.RawMessage(`{"code":"10000","msg":"Success","qr_code":"https://qr.alipay.com/test","out_trade_no":"CR1"}`)
		signature := signAlipayTestBytes(t, alipayPrivate, response)
		_, _ = io.WriteString(w, `{"alipay_trade_precreate_response":`+string(response)+`,"sign":"`+signature+`"}`)
	}))
	defer server.Close()
	client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), server.Client(), AlipayMethods{})
	creditClient := client.(interface {
		PrecreateCreditPayment(context.Context, AlipayPrecreateRequest) (AlipayPrecreateResponse, error)
	})
	got, err := creditClient.PrecreateCreditPayment(context.Background(), AlipayPrecreateRequest{OutTradeNo: "CR1", Subject: "500 Credits", TotalAmount: "1.90", ProductCode: "QR_CODE_OFFLINE"})
	if err != nil {
		t.Fatal(err)
	}
	if got.QRCode != "https://qr.alipay.com/test" {
		t.Fatalf("qr = %q", got.QRCode)
	}
}

func TestAlipayQueryCreditPaymentCarriesProviderPaymentTime(t *testing.T) {
	appPrivate, appPublic := generateAlipayTestKeyPair(t)
	alipayPrivate, alipayPublic := generateAlipayTestKeyPair(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("method"); got != "alipay.trade.query" {
			t.Fatalf("method = %q", got)
		}
		if err := verifyAlipayTestValues(r.Form, appPublic); err != nil {
			t.Fatal(err)
		}
		response := json.RawMessage(`{"code":"10000","msg":"Success","trade_no":"ALI-1","out_trade_no":"CR-1","trade_status":"TRADE_SUCCESS","total_amount":"1.90","send_pay_date":"2026-08-03 14:05:06"}`)
		signature := signAlipayTestBytes(t, alipayPrivate, response)
		_, _ = io.WriteString(w, `{"alipay_trade_query_response":`+string(response)+`,"sign":"`+signature+`"}`)
	}))
	defer server.Close()

	client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), server.Client(), AlipayMethods{})
	creditClient := client.(interface {
		QueryCreditPayment(context.Context, string) (AlipayTransaction, error)
	})
	got, err := creditClient.QueryCreditPayment(context.Background(), "CR-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.OutTradeNo != "CR-1" || got.PaymentTime != "2026-08-03 14:05:06" {
		t.Fatalf("query transaction = %+v", got)
	}
}

func TestAlipayVerifyNotificationRejectsTampering(t *testing.T) {
	alipayPrivate, alipayPublic := generateAlipayTestKeyPair(t)
	client := NewAlipayClientWithHTTP(alipayTestConfig("https://example.invalid", nil, alipayPublic), http.DefaultClient, AlipayMethods{})

	values := url.Values{
		"notify_id":    {"notify_1"},
		"trade_no":     {"trade_1"},
		"total_amount": {"29.90"},
		"sign_type":    {"RSA2"},
	}
	values.Set("sign", signAlipayTestBytes(t, alipayPrivate, []byte(alipayCanonicalValues(values))))
	if err := client.VerifyNotification(values); err != nil {
		t.Fatalf("valid notification rejected: %v", err)
	}

	values.Set("total_amount", "0.01")
	if err := client.VerifyNotification(values); err == nil {
		t.Fatal("tampered notification was accepted")
	}
}

func TestAlipayClientRejectsUnsignedAndBusinessErrorResponses(t *testing.T) {
	appPrivate, _ := generateAlipayTestKeyPair(t)
	_, alipayPublic := generateAlipayTestKeyPair(t)

	tests := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{name: "unsigned response", status: http.StatusOK, body: `{"alipay_test_subscription_create_response":{"code":"10000","msg":"Success"}}`, want: "signature"},
		{name: "http error", status: http.StatusBadGateway, body: `upstream unavailable`, want: "HTTP 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), server.Client(), AlipayMethods{CreateSubscription: "alipay.test.subscription.create"})
			_, err := client.CreateSubscription(context.Background(), AlipaySubscriptionRequest{RequestID: "req_1", PlanID: "p", ProductID: "x", Amount: "1.00"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestAlipayClientHonorsHTTPTimeout(t *testing.T) {
	appPrivate, _ := generateAlipayTestKeyPair(t)
	_, alipayPublic := generateAlipayTestKeyPair(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	client := NewAlipayClientWithHTTP(alipayTestConfig(server.URL, appPrivate, alipayPublic), httpClient, AlipayMethods{CreateSubscription: "alipay.test.subscription.create"})
	_, err := client.CreateSubscription(context.Background(), AlipaySubscriptionRequest{RequestID: "req_1", PlanID: "p", ProductID: "x", Amount: "1.00"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func alipayTestConfig(gateway string, appPrivate *rsa.PrivateKey, alipayPublic *rsa.PublicKey) config.Config {
	cfg := config.Config{
		AlipayAppID: "2021000000000000", AlipayMerchantID: "2088000000000000",
		AlipayGatewayURL: gateway, AlipayNotifyURL: "https://api.example/notify", AlipayReturnURL: "https://app.example/return",
	}
	if appPrivate != nil {
		der, _ := x509.MarshalPKCS8PrivateKey(appPrivate)
		cfg.AlipayPrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	if alipayPublic != nil {
		der, _ := x509.MarshalPKIXPublicKey(alipayPublic)
		cfg.AlipayPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	return cfg
}

func generateAlipayTestKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, &privateKey.PublicKey
}

func signAlipayTestBytes(t *testing.T, privateKey *rsa.PrivateKey, payload []byte) string {
	t.Helper()
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func verifyAlipayTestValues(values url.Values, publicKey *rsa.PublicKey) error {
	signature, err := base64.StdEncoding.DecodeString(values.Get("sign"))
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(alipayCanonicalValues(values)))
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature)
}
