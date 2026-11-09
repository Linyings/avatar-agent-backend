package providers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
)

// Retain only fixed diagnostic categories and a validated request identifier.
// Never persist error strings, headers, request bodies, URLs, or provider bodies.
type contentRequestDiagnostic struct {
	mu        sync.Mutex
	phase     string
	status    int
	requestID string
}

func (d *contentRequestDiagnostic) setPhase(phase string) {
	d.mu.Lock()
	d.phase = phase
	d.mu.Unlock()
}
func (d *contentRequestDiagnostic) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { d.setPhase("dns") },
		ConnectStart:      func(string, string) { d.setPhase("connect") },
		TLSHandshakeStart: func() { d.setPhase("tls") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				d.setPhase("tls_complete")
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				d.setPhase("request_sent")
			}
		},
		GotFirstResponseByte: func() { d.setPhase("response_started") },
	}
}

type contentDiagnosticTransport struct {
	base       http.RoundTripper
	diagnostic *contentRequestDiagnostic
}

func (t contentDiagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if response != nil {
		t.diagnostic.mu.Lock()
		t.diagnostic.status = response.StatusCode
		t.diagnostic.requestID = response.Header.Get("X-Request-Id")
		t.diagnostic.phase = "response_headers"
		t.diagnostic.mu.Unlock()
	}
	return response, err
}

type contentDiagnosticError struct {
	cause                  error
	code, phase, requestID string
	status                 int
}

func (e *contentDiagnosticError) Error() string           { return "模型调用诊断：" + e.code }
func (e *contentDiagnosticError) Unwrap() error           { return e.cause }
func (e *contentDiagnosticError) ProviderHTTPStatus() int { return e.status }

// Only a complete model response proves the text request finished. Transport
// errors and malformed provider envelopes remain uncertain submissions.
func (e *contentDiagnosticError) ProviderOutputReceived() bool {
	return e.code == "model_invalid_json" || e.code == "model_output_truncated"
}
func (e *contentDiagnosticError) ProviderDiagnosticFields() map[string]string {
	return map[string]string{"error_code": e.code, "transport_phase": e.phase, "request_id": e.requestID}
}
func (d *contentRequestDiagnostic) wrap(err error) error {
	if err == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	code := "request_failed"
	var dns *net.DNSError
	var syntax *json.SyntaxError
	var timeout net.Error
	switch {
	case errors.As(err, &dns):
		code = "network_dns"
	case errors.Is(err, context.DeadlineExceeded):
		code = "network_timeout"
	case errors.Is(err, context.Canceled):
		code = "request_canceled"
	case errors.As(err, &timeout) && timeout.Timeout():
		code = "network_timeout"
	case d.status >= 400:
		code = "provider_http_error"
	case errors.As(err, &syntax):
		code = "response_invalid_json"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		code = "connection_interrupted"
	case strings.Contains(err.Error(), "response has no choices"):
		code = "response_no_choices"
	case strings.Contains(err.Error(), "response has no valid narration content"):
		code = "response_empty_content"
	case err.Error() == "模型未返回有效JSON":
		code = "model_invalid_json"
	case err.Error() == "模型输出达到上限被截断":
		code = "model_output_truncated"
	case d.phase == "tls":
		code = "network_tls"
	case d.phase == "connect":
		code = "network_connect"
	}
	return &contentDiagnosticError{cause: err, code: code, phase: d.phase, status: d.status, requestID: d.requestID}
}
