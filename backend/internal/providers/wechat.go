package providers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

type WeChatClient struct {
	cfg        config.Config
	httpClient *http.Client
}

type WeChatNativeOrderRequest struct {
	Description string
	OutTradeNo  string
	TotalAmount float64
	Attach      string
	TimeExpire  time.Time
}

type WeChatNativeOrderResponse struct {
	CodeURL string `json:"code_url"`
}

func (w WeChatClient) RefundCreditPayment(ctx context.Context, outTradeNo, outRefundNo string, amountCNY float64) error {
	body := map[string]any{"out_trade_no": outTradeNo, "out_refund_no": outRefundNo, "amount": map[string]any{"refund": int(math.Round(amountCNY * 100)), "total": int(math.Round(amountCNY * 100)), "currency": "CNY"}}
	payload, _ := json.Marshal(body)
	path := "/v3/refund/domestic/refunds"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.mch.weixin.qq.com"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	auth, err := w.authorization(http.MethodPost, path, string(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wechat refund failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

type WeChatNotification struct {
	ID           string `json:"id"`
	CreateTime   string `json:"create_time"`
	EventType    string `json:"event_type"`
	ResourceType string `json:"resource_type"`
	Resource     struct {
		Algorithm      string `json:"algorithm"`
		Ciphertext     string `json:"ciphertext"`
		AssociatedData string `json:"associated_data"`
		Nonce          string `json:"nonce"`
	} `json:"resource"`
}

type WeChatTransaction struct {
	AppID          string `json:"appid"`
	MchID          string `json:"mchid"`
	OutTradeNo     string `json:"out_trade_no"`
	TransactionID  string `json:"transaction_id"`
	TradeType      string `json:"trade_type"`
	TradeState     string `json:"trade_state"`
	TradeStateDesc string `json:"trade_state_desc"`
	Attach         string `json:"attach"`
	SuccessTime    string `json:"success_time"`
	Amount         struct {
		Total         int    `json:"total"`
		Currency      string `json:"currency"`
		PayerTotal    int    `json:"payer_total"`
		PayerCurrency string `json:"payer_currency"`
	} `json:"amount"`
}

func NewWeChatClient(cfg config.Config) WeChatClient {
	return WeChatClient{cfg: cfg, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (w WeChatClient) Configured() bool {
	return w.cfg.WeChatConfigured()
}

func (w WeChatClient) CreateNativeOrder(ctx context.Context, req WeChatNativeOrderRequest) (WeChatNativeOrderResponse, error) {
	if !w.Configured() {
		return WeChatNativeOrderResponse{}, errors.New("微信支付配置不完整")
	}
	body := map[string]any{
		"appid":        w.cfg.WeChatAppID,
		"mchid":        w.cfg.WeChatMchID,
		"description":  req.Description,
		"out_trade_no": req.OutTradeNo,
		"notify_url":   w.cfg.WeChatNotifyURL,
		"amount": map[string]any{
			"total":    int(math.Round(req.TotalAmount * 100)),
			"currency": "CNY",
		},
	}
	if req.Attach != "" {
		body["attach"] = req.Attach
	}
	if !req.TimeExpire.IsZero() {
		body["time_expire"] = req.TimeExpire.Format(time.RFC3339)
	}
	payload, _ := json.Marshal(body)
	path := "/v3/pay/transactions/native"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.mch.weixin.qq.com"+path, bytes.NewReader(payload))
	if err != nil {
		return WeChatNativeOrderResponse{}, err
	}
	auth, err := w.authorization(http.MethodPost, path, string(payload))
	if err != nil {
		return WeChatNativeOrderResponse{}, err
	}
	httpReq.Header.Set("Authorization", auth)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := w.httpClient.Do(httpReq)
	if err != nil {
		return WeChatNativeOrderResponse{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WeChatNativeOrderResponse{}, fmt.Errorf("微信支付下单失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out WeChatNativeOrderResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return WeChatNativeOrderResponse{}, err
	}
	return out, nil
}

func (w WeChatClient) QueryTransactionByOutTradeNo(ctx context.Context, outTradeNo string) (WeChatTransaction, error) {
	if !w.Configured() {
		return WeChatTransaction{}, errors.New("微信支付配置不完整")
	}
	outTradeNo = strings.TrimSpace(outTradeNo)
	if outTradeNo == "" {
		return WeChatTransaction{}, errors.New("商户订单号为空")
	}
	path := "/v3/pay/transactions/out-trade-no/" + url.PathEscape(outTradeNo)
	query := "mchid=" + url.QueryEscape(w.cfg.WeChatMchID)
	requestTarget := path + "?" + query
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.mch.weixin.qq.com"+requestTarget, nil)
	if err != nil {
		return WeChatTransaction{}, err
	}
	auth, err := w.authorization(http.MethodGet, requestTarget, "")
	if err != nil {
		return WeChatTransaction{}, err
	}
	httpReq.Header.Set("Authorization", auth)
	httpReq.Header.Set("Accept", "application/json")
	resp, err := w.httpClient.Do(httpReq)
	if err != nil {
		return WeChatTransaction{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WeChatTransaction{}, fmt.Errorf("微信支付查单失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var tx WeChatTransaction
	if err := json.Unmarshal(respBody, &tx); err != nil {
		return WeChatTransaction{}, err
	}
	return tx, nil
}

func (w WeChatClient) DecryptNotification(payload []byte) (WeChatTransaction, error) {
	var note WeChatNotification
	if err := json.Unmarshal(payload, &note); err != nil {
		return WeChatTransaction{}, err
	}
	block, err := aes.NewCipher([]byte(w.cfg.WeChatAPIV3Key))
	if err != nil {
		return WeChatTransaction{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return WeChatTransaction{}, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(note.Resource.Ciphertext)
	if err != nil {
		return WeChatTransaction{}, err
	}
	plain, err := aead.Open(nil, []byte(note.Resource.Nonce), ciphertext, []byte(note.Resource.AssociatedData))
	if err != nil {
		return WeChatTransaction{}, err
	}
	var tx WeChatTransaction
	if err := json.Unmarshal(plain, &tx); err != nil {
		return WeChatTransaction{}, err
	}
	return tx, nil
}

func (w WeChatClient) VerifyNotificationSignature(timestamp, nonce string, body []byte, signatureB64, serialNo string) error {
	if !w.cfg.WeChatVerifySignature {
		return nil
	}
	if timestamp == "" || nonce == "" || len(body) == 0 || signatureB64 == "" || serialNo == "" {
		return errors.New("微信回调验签参数不完整")
	}
	cert, err := w.platformCertificate(serialNo)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return err
	}
	message := timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	sum := sha256.Sum256([]byte(message))
	publicKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("微信平台证书不是 RSA 公钥")
	}
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, sum[:], signature); err != nil {
		return fmt.Errorf("微信回调签名验证失败: %w", err)
	}
	return nil
}

func (w WeChatClient) platformCertificate(serialNo string) (*x509.Certificate, error) {
	normalized := normalizeSerial(serialNo)
	if normalized == "" {
		return nil, errors.New("微信平台证书序列号为空")
	}
	entries, err := os.ReadDir(w.cfg.WeChatCertDir)
	if err != nil {
		return nil, fmt.Errorf("微信平台证书目录不可用: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(w.cfg.WeChatCertDir, entry.Name()))
		if err != nil {
			continue
		}
		cert, err := parseCertificate(data)
		if err != nil {
			continue
		}
		if normalizeSerial(cert.SerialNumber.Text(16)) == normalized {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("未找到微信平台证书: %s", serialNo)
}

func parseCertificate(data []byte) (*x509.Certificate, error) {
	if block, _ := pem.Decode(data); block != nil {
		return x509.ParseCertificate(block.Bytes)
	}
	return x509.ParseCertificate(data)
}

func normalizeSerial(value string) string {
	return strings.TrimLeft(strings.ToUpper(strings.TrimSpace(value)), "0")
}

func (w WeChatClient) authorization(method, path, body string) (string, error) {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce, err := randomHex(16)
	if err != nil {
		return "", err
	}
	signature, err := w.sign(method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + body + "\n")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`, w.cfg.WeChatMchID, nonce, signature, timestamp, w.cfg.WeChatCertSerialNo), nil
}

func (w WeChatClient) sign(message string) (string, error) {
	key, err := w.privateKey()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (w WeChatClient) privateKey() (*rsa.PrivateKey, error) {
	content := w.cfg.WeChatPrivateKey
	if content == "" && w.cfg.WeChatPrivateKeyFile != "" {
		data, err := os.ReadFile(w.cfg.WeChatPrivateKeyFile)
		if err != nil {
			return nil, err
		}
		content = string(data)
	}
	block, _ := pem.Decode([]byte(content))
	if block == nil {
		return nil, errors.New("商户私钥 PEM 解析失败")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("商户私钥不是 RSA 私钥")
	}
	return key, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const alphabet = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, b := range buf {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&0x0f]
	}
	return string(out), nil
}
