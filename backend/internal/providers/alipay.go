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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

const alipayMaxResponseBytes = 1 << 20

var ErrAlipayCapabilityUnavailable = errors.New("Alipay capability method is not configured")

type AlipayClient interface {
	Configured() bool
	CreateSubscription(context.Context, AlipaySubscriptionRequest) (AlipaySubscriptionCheckout, error)
	GetSubscription(context.Context, string) (AlipaySubscription, error)
	GetTransaction(context.Context, string) (AlipayTransaction, error)
	CreatePortal(context.Context, string) (string, error)
	CancelSubscription(context.Context, string) error
	VerifyNotification(url.Values) error
}

// AlipayMethods is populated from the product documentation made available after approval.
// Keeping it injectable prevents provisional method names from leaking into business logic.
type AlipayMethods struct {
	CreateSubscription string
	GetSubscription    string
	GetTransaction     string
	CreatePortal       string
	CancelSubscription string
}

type AlipaySubscriptionRequest struct {
	RequestID string `json:"request_id"`
	PlanID    string `json:"plan_id"`
	ProductID string `json:"product_id"`
	Amount    string `json:"total_amount"`
	UserID    string `json:"external_user_id,omitempty"`
}

type AlipaySubscriptionCheckout struct {
	Code        string `json:"code"`
	Message     string `json:"msg"`
	SubCode     string `json:"sub_code"`
	SubMessage  string `json:"sub_msg"`
	CheckoutURL string `json:"checkout_url"`
	RequestID   string `json:"request_id"`
	AgreementID string `json:"agreement_id"`
}

type AlipaySubscription struct {
	Code           string    `json:"code"`
	Message        string    `json:"msg"`
	SubCode        string    `json:"sub_code"`
	SubMessage     string    `json:"sub_msg"`
	AgreementID    string    `json:"agreement_id"`
	SubscriptionID string    `json:"subscription_id"`
	Status         string    `json:"status"`
	PlanID         string    `json:"plan_id"`
	ProductID      string    `json:"product_id"`
	Amount         string    `json:"total_amount"`
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	NextChargeAt   time.Time `json:"next_charge_at"`
}

type AlipayTransaction struct {
	Code           string `json:"code"`
	Message        string `json:"msg"`
	SubCode        string `json:"sub_code"`
	SubMessage     string `json:"sub_msg"`
	AppID          string `json:"app_id"`
	SellerID       string `json:"seller_id"`
	OutTradeNo     string `json:"out_trade_no"`
	TransactionID  string `json:"trade_no"`
	AgreementID    string `json:"agreement_id"`
	SubscriptionID string `json:"subscription_id"`
	Status         string `json:"trade_status"`
	Amount         string `json:"total_amount"`
	PaymentTime    string `json:"send_pay_date"`
	ProductID      string `json:"product_id"`
}

type AlipayPrecreateRequest struct {
	OutTradeNo  string `json:"out_trade_no"`
	Subject     string `json:"subject"`
	TotalAmount string `json:"total_amount"`
	ProductCode string `json:"product_code"`
}
type AlipayPrecreateResponse struct {
	Code       string `json:"code"`
	Message    string `json:"msg"`
	SubCode    string `json:"sub_code"`
	SubMessage string `json:"sub_msg"`
	QRCode     string `json:"qr_code"`
	OutTradeNo string `json:"out_trade_no"`
}

type AlipayPagePaymentRequest struct {
	OutTradeNo  string `json:"out_trade_no"`
	Subject     string `json:"subject"`
	TotalAmount string `json:"total_amount"`
}

func (c *alipayGatewayClient) PagePaymentURL(ctx context.Context, req AlipayPagePaymentRequest) (string, error) {
	privateKey, err := c.appPrivateKey()
	if err != nil {
		return "", err
	}
	bizContent := map[string]any{
		"out_trade_no": strings.TrimSpace(req.OutTradeNo),
		"total_amount": strings.TrimSpace(req.TotalAmount),
		"subject":      strings.TrimSpace(req.Subject),
		"product_code": "FAST_INSTANT_TRADE_PAY",
	}
	bizJSON, err := json.Marshal(bizContent)
	if err != nil {
		return "", fmt.Errorf("marshal Alipay biz_content: %w", err)
	}
	values := url.Values{
		"app_id":      {strings.TrimSpace(c.cfg.AlipayAppID)},
		"method":      {"alipay.trade.page.pay"},
		"format":      {"JSON"},
		"charset":     {"utf-8"},
		"sign_type":   {"RSA2"},
		"timestamp":   {time.Now().Format("2006-01-02 15:04:05")},
		"version":     {"1.0"},
		"notify_url":  {strings.TrimSpace(c.cfg.AlipayNotifyURL)},
		"return_url":  {strings.TrimSpace(c.cfg.AlipayReturnURL)},
		"biz_content": {string(bizJSON)},
	}
	values.Set("sign", signAlipayBytes(privateKey, []byte(alipayCanonicalValues(values))))
	gateway := strings.TrimSpace(c.cfg.AlipayGatewayURL)
	return gateway + "?" + values.Encode(), nil
}

func (c *alipayGatewayClient) PrecreateCreditPayment(ctx context.Context, req AlipayPrecreateRequest) (AlipayPrecreateResponse, error) {
	var out AlipayPrecreateResponse
	err := c.execute(ctx, "alipay.trade.precreate", req, &out)
	if err == nil && strings.TrimSpace(out.QRCode) == "" {
		err = errors.New("Alipay response did not include qr_code")
	}
	return out, err
}

func (c *alipayGatewayClient) QueryCreditPayment(ctx context.Context, outTradeNo string) (AlipayTransaction, error) {
	var out AlipayTransaction
	err := c.execute(ctx, "alipay.trade.query", map[string]string{"out_trade_no": outTradeNo}, &out)
	if err != nil {
		return out, err
	}
	appID := strings.TrimSpace(c.cfg.AlipayAppID)
	sellerID := strings.TrimSpace(c.cfg.AlipayMerchantID)
	if out.AppID != "" && out.AppID != appID {
		return AlipayTransaction{}, errors.New("Alipay query app_id mismatch")
	}
	if out.SellerID != "" && out.SellerID != sellerID {
		return AlipayTransaction{}, errors.New("Alipay query seller_id mismatch")
	}
	out.AppID = appID
	out.SellerID = sellerID
	return out, err
}

func (c *alipayGatewayClient) RefundCreditPayment(ctx context.Context, outTradeNo, refundAmount, outRequestNo string) error {
	var out alipayBusinessResponse
	return c.execute(ctx, "alipay.trade.refund", map[string]string{"out_trade_no": outTradeNo, "refund_amount": refundAmount, "out_request_no": outRequestNo}, &out)
}

type alipayGatewayClient struct {
	cfg        config.Config
	httpClient *http.Client
	methods    AlipayMethods
}

func NewAlipayClient(cfg config.Config, methods AlipayMethods) AlipayClient {
	return NewAlipayClientWithHTTP(cfg, &http.Client{Timeout: 30 * time.Second}, methods)
}

func NewAlipayClientWithHTTP(cfg config.Config, httpClient *http.Client, methods AlipayMethods) AlipayClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &alipayGatewayClient{cfg: cfg, httpClient: httpClient, methods: methods}
}

func (c *alipayGatewayClient) Configured() bool {
	return c.cfg.AlipayConfigured()
}

func (c *alipayGatewayClient) CreateSubscription(ctx context.Context, req AlipaySubscriptionRequest) (AlipaySubscriptionCheckout, error) {
	var out AlipaySubscriptionCheckout
	err := c.execute(ctx, c.methods.CreateSubscription, req, &out)
	return out, err
}

func (c *alipayGatewayClient) GetSubscription(ctx context.Context, subscriptionID string) (AlipaySubscription, error) {
	var out AlipaySubscription
	err := c.execute(ctx, c.methods.GetSubscription, map[string]string{"subscription_id": subscriptionID}, &out)
	return out, err
}

func (c *alipayGatewayClient) GetTransaction(ctx context.Context, transactionID string) (AlipayTransaction, error) {
	var out AlipayTransaction
	err := c.execute(ctx, c.methods.GetTransaction, map[string]string{"trade_no": transactionID}, &out)
	return out, err
}

func (c *alipayGatewayClient) CreatePortal(ctx context.Context, agreementID string) (string, error) {
	var out struct {
		Code       string `json:"code"`
		Message    string `json:"msg"`
		SubCode    string `json:"sub_code"`
		SubMessage string `json:"sub_msg"`
		PortalURL  string `json:"portal_url"`
	}
	if err := c.execute(ctx, c.methods.CreatePortal, map[string]string{"agreement_id": agreementID}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.PortalURL) == "" {
		return "", errors.New("Alipay response did not include portal_url")
	}
	return out.PortalURL, nil
}

func (c *alipayGatewayClient) CancelSubscription(ctx context.Context, agreementID string) error {
	var out alipayBusinessResponse
	return c.execute(ctx, c.methods.CancelSubscription, map[string]string{"agreement_id": agreementID}, &out)
}

func (c *alipayGatewayClient) VerifyNotification(values url.Values) error {
	publicKey, err := c.alipayPublicKey()
	if err != nil {
		return err
	}
	signature := strings.TrimSpace(values.Get("sign"))
	if signature == "" {
		return errors.New("Alipay notification signature is missing")
	}
	return verifyAlipaySignature(publicKey, []byte(alipayCanonicalValues(values)), signature)
}

func (c *alipayGatewayClient) execute(ctx context.Context, method string, bizContent any, target any) error {
	method = strings.TrimSpace(method)
	if method == "" {
		return ErrAlipayCapabilityUnavailable
	}
	privateKey, err := c.appPrivateKey()
	if err != nil {
		return err
	}
	bizJSON, err := json.Marshal(bizContent)
	if err != nil {
		return fmt.Errorf("marshal Alipay biz_content: %w", err)
	}
	values := url.Values{
		"app_id":      {strings.TrimSpace(c.cfg.AlipayAppID)},
		"method":      {method},
		"format":      {"JSON"},
		"charset":     {"utf-8"},
		"sign_type":   {"RSA2"},
		"timestamp":   {time.Now().Format("2006-01-02 15:04:05")},
		"version":     {"1.0"},
		"notify_url":  {strings.TrimSpace(c.cfg.AlipayNotifyURL)},
		"return_url":  {strings.TrimSpace(c.cfg.AlipayReturnURL)},
		"biz_content": {string(bizJSON)},
	}
	values.Set("sign", signAlipayBytes(privateKey, []byte(alipayCanonicalValues(values))))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(c.cfg.AlipayGatewayURL), strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create Alipay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Alipay request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, alipayMaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Alipay response: %w", err)
	}
	if len(body) > alipayMaxResponseBytes {
		return errors.New("Alipay response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Alipay API request failed: HTTP %d", resp.StatusCode)
	}
	return c.decodeVerifiedResponse(method, body, target)
}

func (c *alipayGatewayClient) decodeVerifiedResponse(method string, body []byte, target any) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Alipay response envelope: %w", err)
	}
	responseKey := strings.ReplaceAll(method, ".", "_") + "_response"
	responseBody := envelope[responseKey]
	if len(responseBody) == 0 {
		return fmt.Errorf("Alipay response field %s is missing", responseKey)
	}
	var signature string
	if raw := envelope["sign"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &signature)
	}
	if signature == "" {
		return errors.New("Alipay response signature is missing")
	}
	publicKey, err := c.alipayPublicKey()
	if err != nil {
		return err
	}
	if err := verifyAlipaySignature(publicKey, responseBody, signature); err != nil {
		return fmt.Errorf("Alipay response signature verification failed: %w", err)
	}
	var common alipayBusinessResponse
	if err := json.Unmarshal(responseBody, &common); err != nil {
		return fmt.Errorf("decode Alipay response: %w", err)
	}
	if common.Code != "10000" {
		return &AlipayBusinessError{Code: common.Code, Message: common.Message, SubCode: common.SubCode, SubMessage: common.SubMessage}
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(responseBody, target)
}

type alipayBusinessResponse struct {
	Code       string `json:"code"`
	Message    string `json:"msg"`
	SubCode    string `json:"sub_code"`
	SubMessage string `json:"sub_msg"`
}

type AlipayBusinessError struct {
	Code       string
	Message    string
	SubCode    string
	SubMessage string
}

func (e *AlipayBusinessError) Error() string {
	return fmt.Sprintf("Alipay business error: code=%s sub_code=%s message=%s", e.Code, e.SubCode, firstNonEmpty(e.SubMessage, e.Message))
}

func alipayCanonicalValues(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key, entries := range values {
		if key == "sign" || len(entries) == 0 || entries[0] == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}

func signAlipayBytes(privateKey *rsa.PrivateKey, payload []byte) string {
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func verifyAlipaySignature(publicKey *rsa.PublicKey, payload []byte, encodedSignature string) error {
	signature, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	digest := sha256.Sum256(payload)
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature)
}

func (c *alipayGatewayClient) appPrivateKey() (*rsa.PrivateKey, error) {
	data, err := alipayKeyMaterial(c.cfg.AlipayPrivateKey, c.cfg.AlipayPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load Alipay application private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("parse Alipay application private key: invalid PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("parse Alipay application private key: unsupported RSA key")
	}
	return key, nil
}

func (c *alipayGatewayClient) alipayPublicKey() (*rsa.PublicKey, error) {
	data, err := alipayKeyMaterial(c.cfg.AlipayPublicKey, c.cfg.AlipayPublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load Alipay public key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("parse Alipay public key: invalid PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("parse Alipay public key: unsupported RSA key")
}

func alipayKeyMaterial(inline, path string) ([]byte, error) {
	if value := strings.TrimSpace(inline); value != "" {
		return []byte(value), nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("key is not configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}
