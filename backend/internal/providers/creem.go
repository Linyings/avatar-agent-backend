package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

type CreemClient struct {
	cfg        config.Config
	httpClient *http.Client
}

type CreemCheckoutRequest struct {
	ProductID    string         `json:"product_id"`
	RequestID    string         `json:"request_id,omitempty"`
	DiscountCode string         `json:"discount_code,omitempty"`
	SuccessURL   string         `json:"success_url,omitempty"`
	Customer     *CreemCustomer `json:"customer,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type CreemCustomer struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
}

type CreemCheckoutResponse struct {
	ID          string         `json:"id"`
	CheckoutURL string         `json:"checkout_url"`
	ProductID   string         `json:"product_id"`
	Status      string         `json:"status"`
	RequestID   string         `json:"request_id"`
	Metadata    map[string]any `json:"metadata"`
}

type CreemPortalResponse struct {
	CustomerPortalLink string `json:"customer_portal_link"`
	CustomerPortalURL  string `json:"customerPortalLink"`
	URL                string `json:"url"`
}

type CreemSubscriptionUpgradeRequest struct {
	ProductID      string `json:"product_id"`
	UpdateBehavior string `json:"update_behavior"`
}

func NewCreemClient(cfg config.Config) CreemClient {
	return CreemClient{cfg: cfg, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c CreemClient) Configured() bool {
	return c.cfg.CreemConfigured()
}

func (c CreemClient) VerifyWebhookSignature(payload []byte, signature string) error {
	secret := strings.TrimSpace(c.cfg.CreemWebhookSecret)
	if secret == "" {
		return errors.New("Creem webhook secret 未配置")
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return errors.New("缺少 Creem webhook 签名")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return errors.New("Creem webhook 签名验证失败")
	}
	return nil
}

func (c CreemClient) CreateCheckout(ctx context.Context, req CreemCheckoutRequest) (CreemCheckoutResponse, error) {
	var out CreemCheckoutResponse
	if !c.Configured() {
		return out, errors.New("Creem 配置不完整")
	}
	if strings.TrimSpace(req.ProductID) == "" {
		return out, errors.New("缺少 Creem product_id")
	}
	if err := c.post(ctx, "/v1/checkouts", req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c CreemClient) GetCheckout(ctx context.Context, checkoutID string) (map[string]any, error) {
	var out map[string]any
	if !c.Configured() {
		return out, errors.New("Creem 配置不完整")
	}
	checkoutID = strings.TrimSpace(checkoutID)
	if checkoutID == "" {
		return out, errors.New("缺少 Creem checkout_id")
	}
	if err := c.get(ctx, "/v1/checkouts?checkout_id="+url.QueryEscape(checkoutID), &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c CreemClient) CancelSubscription(ctx context.Context, subscriptionID string, scheduled bool) error {
	if !c.Configured() {
		return errors.New("Creem 配置不完整")
	}
	mode := "immediate"
	if scheduled {
		mode = "scheduled"
	}
	body := map[string]any{"mode": mode, "onExecute": "cancel"}
	return c.post(ctx, "/v1/subscriptions/"+subscriptionID+"/cancel", body, nil)
}

func (c CreemClient) UpgradeSubscription(ctx context.Context, subscriptionID, productID, updateBehavior string) (map[string]any, error) {
	var out map[string]any
	if !c.Configured() {
		return out, errors.New("Creem 配置不完整")
	}
	if strings.TrimSpace(subscriptionID) == "" {
		return out, errors.New("缺少 Creem subscription_id")
	}
	if strings.TrimSpace(productID) == "" {
		return out, errors.New("缺少 Creem product_id")
	}
	if updateBehavior == "" {
		updateBehavior = "proration-charge-immediately"
	}
	body := CreemSubscriptionUpgradeRequest{
		ProductID:      productID,
		UpdateBehavior: updateBehavior,
	}
	if err := c.post(ctx, "/v1/subscriptions/"+subscriptionID+"/upgrade", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c CreemClient) CreateCustomerPortal(ctx context.Context, customerID string) (string, error) {
	if !c.Configured() {
		return "", errors.New("Creem 配置不完整")
	}
	if strings.TrimSpace(customerID) == "" {
		return "", errors.New("缺少 Creem customer_id")
	}
	var out CreemPortalResponse
	if err := c.post(ctx, "/v1/customers/billing", map[string]any{"customer_id": customerID}, &out); err != nil {
		fallbackErr := c.post(ctx, "/v1/customer-portal", map[string]any{"customer_id": customerID}, &out)
		if fallbackErr != nil {
			return "", fmt.Errorf("Creem customer billing 创建失败: %v; fallback: %v", err, fallbackErr)
		}
	}
	if out.CustomerPortalLink != "" {
		return out.CustomerPortalLink, nil
	}
	if out.CustomerPortalURL != "" {
		return out.CustomerPortalURL, nil
	}
	if out.URL != "" {
		return out.URL, nil
	}
	return "", errors.New("Creem 未返回 customer portal 链接")
}

func (c CreemClient) get(ctx context.Context, path string, target any) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("x-api-key", c.cfg.CreemAPIKey)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Creem API 请求失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if target == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, target)
}

func (c CreemClient) post(ctx context.Context, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("x-api-key", c.cfg.CreemAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Creem API 请求失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if target == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, target)
}

func (c CreemClient) baseURL() string {
	if c.cfg.CreemTestMode {
		return "https://test-api.creem.io"
	}
	return "https://api.creem.io"
}
