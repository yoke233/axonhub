package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/contexts"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestCodexHandlers_StartOAuth_InvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  httpclient.NewHttpClient(),
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(contexts.WithProjectID(c.Request.Context(), 123))
		c.Next()
	})
	router.POST("/admin/codex/oauth/start", h.StartOAuth)

	req := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/start", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid request format")
}

func TestCodexHandlers_StartOAuth_DoesNotIncludeOriginatorParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  httpclient.NewHttpClient(),
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(contexts.WithProjectID(c.Request.Context(), 123))
		c.Next()
	})
	router.POST("/admin/codex/oauth/start", h.StartOAuth)

	req := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/start", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp StartCodexOAuthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.SessionID)

	parsed, err := url.Parse(resp.AuthURL)
	require.NoError(t, err)
	query := parsed.Query()
	require.Empty(t, query.Get("originator"))
	require.Equal(t, codex.ClientID, query.Get("client_id"))
	require.Equal(t, codex.RedirectURI, query.Get("redirect_uri"))
	require.Equal(t, "true", query.Get("codex_cli_simplified_flow"))
	require.Equal(t, "login", query.Get("prompt"))
	require.Equal(t, resp.SessionID, query.Get("state"))
}

func TestCodexHandlers_DeviceFlow_Pending(t *testing.T) {
	gin.SetMode(gin.TestMode)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case codexDeviceUserCodeURL:
			return jsonResponse(http.StatusOK, `{"device_auth_id":"dev-1","user_code":"ABCD-EFGH","interval":2}`), nil
		case codexDeviceTokenURL:
			return jsonResponse(http.StatusForbidden, `{"error":"authorization_pending"}`), nil
		default:
			return http.DefaultTransport.RoundTrip(req)
		}
	})

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  httpclient.NewHttpClientWithClient(&http.Client{Transport: transport}),
	})

	router := gin.New()
	router.POST("/admin/codex/device/start", h.StartDevice)
	router.POST("/admin/codex/device/poll", h.PollDevice)

	startReq := httptest.NewRequest(http.MethodPost, "/admin/codex/device/start", nil)
	startReq.Header.Set("Content-Type", "application/json")
	startW := httptest.NewRecorder()
	router.ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp StartCodexDeviceResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))
	require.NotEmpty(t, startResp.SessionID)
	require.Equal(t, "ABCD-EFGH", startResp.UserCode)
	require.Equal(t, codexDeviceVerificationURL, startResp.VerificationURI)
	require.Equal(t, 2, startResp.Interval)

	pollBody, err := json.Marshal(PollCodexDeviceRequest{SessionID: startResp.SessionID})
	require.NoError(t, err)
	pollReq := httptest.NewRequest(http.MethodPost, "/admin/codex/device/poll", bytes.NewBuffer(pollBody))
	pollReq.Header.Set("Content-Type", "application/json")
	pollW := httptest.NewRecorder()
	router.ServeHTTP(pollW, pollReq)
	require.Equal(t, http.StatusAccepted, pollW.Code)
	require.Contains(t, pollW.Body.String(), `"pending"`)
}

func TestCodexHandlers_DeviceFlow_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var tokenExchangeBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case codexDeviceUserCodeURL:
			return jsonResponse(http.StatusOK, `{"device_auth_id":"dev-1","usercode":"WXYZ-1234","interval":"3"}`), nil
		case codexDeviceTokenURL:
			return jsonResponse(http.StatusOK, `{"authorization_code":"auth-code","code_verifier":"verifier","code_challenge":"challenge"}`), nil
		case codex.TokenURL:
			var err error
			tokenExchangeBody, err = io.ReadAll(req.Body)
			require.NoError(t, err)
			_ = req.Body.Close()
			return jsonResponse(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","id_token":"id","expires_in":3600,"token_type":"bearer","scope":"openid email"}`), nil
		default:
			return http.DefaultTransport.RoundTrip(req)
		}
	})

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  httpclient.NewHttpClientWithClient(&http.Client{Transport: transport}),
	})

	router := gin.New()
	router.POST("/admin/codex/device/start", h.StartDevice)
	router.POST("/admin/codex/device/poll", h.PollDevice)

	startReq := httptest.NewRequest(http.MethodPost, "/admin/codex/device/start", nil)
	startReq.Header.Set("Content-Type", "application/json")
	startW := httptest.NewRecorder()
	router.ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp StartCodexDeviceResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))
	require.Equal(t, "WXYZ-1234", startResp.UserCode)
	require.Equal(t, 3, startResp.Interval)

	pollBody, err := json.Marshal(PollCodexDeviceRequest{SessionID: startResp.SessionID})
	require.NoError(t, err)
	pollReq := httptest.NewRequest(http.MethodPost, "/admin/codex/device/poll", bytes.NewBuffer(pollBody))
	pollReq.Header.Set("Content-Type", "application/json")
	pollW := httptest.NewRecorder()
	router.ServeHTTP(pollW, pollReq)
	require.Equal(t, http.StatusOK, pollW.Code)

	form, err := url.ParseQuery(string(tokenExchangeBody))
	require.NoError(t, err)
	require.Equal(t, "auth-code", form.Get("code"))
	require.Equal(t, "verifier", form.Get("code_verifier"))
	require.Equal(t, codexDeviceTokenExchangeRedirectURI, form.Get("redirect_uri"))

	var pollResp PollCodexDeviceResponse
	require.NoError(t, json.Unmarshal(pollW.Body.Bytes(), &pollResp))
	require.Equal(t, "success", pollResp.Status)
	require.Contains(t, pollResp.Credentials, `"access_token":"access"`)
	require.Contains(t, pollResp.Credentials, `"refresh_token":"refresh"`)
}

func TestCodexHandlers_Exchange_StateDeletedOnTokenExchangeFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var tokenCalls int

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"bad_gateway"}`))
	}))
	t.Cleanup(tokenServer.Close)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == codex.TokenURL {
			proxyReq, err := http.NewRequestWithContext(req.Context(), req.Method, tokenServer.URL, req.Body)
			if err != nil {
				return nil, err
			}

			proxyReq.Header = req.Header.Clone()

			return http.DefaultTransport.RoundTrip(proxyReq)
		}

		return http.DefaultTransport.RoundTrip(req)
	})

	hc := httpclient.NewHttpClientWithClient(&http.Client{Transport: transport})

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  hc,
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(contexts.WithProjectID(c.Request.Context(), 123))
		c.Next()
	})
	router.POST("/admin/codex/oauth/start", h.StartOAuth)
	router.POST("/admin/codex/oauth/exchange", h.Exchange)

	startReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/start", bytes.NewBufferString("{}"))
	startReq.Header.Set("Content-Type", "application/json")

	startW := httptest.NewRecorder()
	router.ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp StartCodexOAuthResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))
	require.NotEmpty(t, startResp.SessionID)

	exchangeBody, err := json.Marshal(ExchangeCodexOAuthRequest{
		SessionID:   startResp.SessionID,
		CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=" + startResp.SessionID,
	})
	require.NoError(t, err)

	exchangeReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")

	exchangeW := httptest.NewRecorder()
	router.ServeHTTP(exchangeW, exchangeReq)
	require.Equal(t, http.StatusBadGateway, exchangeW.Code)
	require.Equal(t, 1, tokenCalls)

	exchangeReq2 := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody))
	exchangeReq2.Header.Set("Content-Type", "application/json")

	exchangeW2 := httptest.NewRecorder()
	router.ServeHTTP(exchangeW2, exchangeReq2)
	require.Equal(t, http.StatusBadRequest, exchangeW2.Code)
	require.Contains(t, exchangeW2.Body.String(), "invalid or expired oauth session")
}

func TestCodexHandlers_Exchange_RejectsStateMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"bearer"}`))
	}))
	t.Cleanup(tokenServer.Close)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == codex.TokenURL {
			body, _ := io.ReadAll(req.Body)
			_ = req.Body.Close()

			proxyReq, err := http.NewRequestWithContext(req.Context(), req.Method, tokenServer.URL, bytes.NewBuffer(body))
			if err != nil {
				return nil, err
			}

			proxyReq.Header = req.Header.Clone()

			return http.DefaultTransport.RoundTrip(proxyReq)
		}

		return http.DefaultTransport.RoundTrip(req)
	})

	hc := httpclient.NewHttpClientWithClient(&http.Client{Transport: transport})

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  hc,
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(contexts.WithProjectID(c.Request.Context(), 123))
		c.Next()
	})
	router.POST("/admin/codex/oauth/start", h.StartOAuth)
	router.POST("/admin/codex/oauth/exchange", h.Exchange)

	startReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/start", bytes.NewBufferString("{}"))
	startReq.Header.Set("Content-Type", "application/json")

	startW := httptest.NewRecorder()
	router.ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp StartCodexOAuthResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))
	require.NotEmpty(t, startResp.SessionID)

	exchangeBody, err := json.Marshal(ExchangeCodexOAuthRequest{
		SessionID:   startResp.SessionID,
		CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=mismatch",
	})
	require.NoError(t, err)

	exchangeReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")

	exchangeW := httptest.NewRecorder()
	router.ServeHTTP(exchangeW, exchangeReq)
	require.Equal(t, http.StatusBadRequest, exchangeW.Code)
	require.Contains(t, exchangeW.Body.String(), "oauth state mismatch")

	exchangeBody2, err := json.Marshal(ExchangeCodexOAuthRequest{
		SessionID:   startResp.SessionID,
		CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=" + startResp.SessionID,
	})
	require.NoError(t, err)

	exchangeReq2 := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody2))
	exchangeReq2.Header.Set("Content-Type", "application/json")

	exchangeW2 := httptest.NewRecorder()
	router.ServeHTTP(exchangeW2, exchangeReq2)
	require.Equal(t, http.StatusBadRequest, exchangeW2.Code)
	require.Contains(t, exchangeW2.Body.String(), "invalid or expired oauth session")
}

func TestCodexHandlers_Exchange_DeletesStateOnSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"bearer"}`))
	}))
	t.Cleanup(tokenServer.Close)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == codex.TokenURL {
			body, _ := io.ReadAll(req.Body)
			_ = req.Body.Close()

			proxyReq, err := http.NewRequestWithContext(req.Context(), req.Method, tokenServer.URL, bytes.NewBuffer(body))
			if err != nil {
				return nil, err
			}

			proxyReq.Header = req.Header.Clone()

			return http.DefaultTransport.RoundTrip(proxyReq)
		}

		return http.DefaultTransport.RoundTrip(req)
	})

	hc := httpclient.NewHttpClientWithClient(&http.Client{Transport: transport})

	h := NewCodexHandlers(CodexHandlersParams{
		CacheConfig: xcache.Config{Mode: xcache.ModeMemory},
		HttpClient:  hc,
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(contexts.WithProjectID(c.Request.Context(), 123))
		c.Next()
	})
	router.POST("/admin/codex/oauth/start", h.StartOAuth)
	router.POST("/admin/codex/oauth/exchange", h.Exchange)

	startReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/start", bytes.NewBufferString("{}"))
	startReq.Header.Set("Content-Type", "application/json")

	startW := httptest.NewRecorder()
	router.ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusOK, startW.Code)

	var startResp StartCodexOAuthResponse
	require.NoError(t, json.Unmarshal(startW.Body.Bytes(), &startResp))

	exchangeBody, err := json.Marshal(ExchangeCodexOAuthRequest{
		SessionID:   startResp.SessionID,
		CallbackURL: "http://localhost:1455/auth/callback?code=test-code&state=" + startResp.SessionID,
	})
	require.NoError(t, err)

	exchangeReq := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")

	exchangeW := httptest.NewRecorder()
	router.ServeHTTP(exchangeW, exchangeReq)
	require.Equal(t, http.StatusOK, exchangeW.Code)

	exchangeReq2 := httptest.NewRequest(http.MethodPost, "/admin/codex/oauth/exchange", bytes.NewBuffer(exchangeBody))
	exchangeReq2.Header.Set("Content-Type", "application/json")

	exchangeW2 := httptest.NewRecorder()
	router.ServeHTTP(exchangeW2, exchangeReq2)
	require.Equal(t, http.StatusBadRequest, exchangeW2.Code)
	require.Contains(t, exchangeW2.Body.String(), "invalid or expired oauth session")
}
