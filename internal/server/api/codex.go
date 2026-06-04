package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

type CodexHandlersParams struct {
	fx.In

	CacheConfig xcache.Config
	HttpClient  *httpclient.HttpClient
}

type CodexHandlers struct {
	stateCache  xcache.Cache[codexOAuthState]
	deviceCache xcache.Cache[codexDeviceState]
	httpClient  *httpclient.HttpClient
}

func NewCodexHandlers(params CodexHandlersParams) *CodexHandlers {
	return &CodexHandlers{
		stateCache:  xcache.NewFromConfig[codexOAuthState](params.CacheConfig),
		deviceCache: xcache.NewFromConfig[codexDeviceState](params.CacheConfig),
		httpClient:  params.HttpClient,
	}
}

type StartCodexOAuthRequest struct{}

type StartCodexOAuthResponse struct {
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
}

type StartCodexDeviceResponse struct {
	SessionID       string `json:"session_id"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type StartCodexDeviceRequest struct {
	Proxy *httpclient.ProxyConfig `json:"proxy,omitempty"`
}

type PollCodexDeviceRequest struct {
	SessionID string                  `json:"session_id" binding:"required"`
	Proxy     *httpclient.ProxyConfig `json:"proxy,omitempty"`
}

type PollCodexDeviceResponse struct {
	Status      string `json:"status"`
	Credentials string `json:"credentials,omitempty"`
}

type codexOAuthState struct {
	CodeVerifier string `json:"code_verifier"`
	CreatedAt    int64  `json:"created_at"`
}

const (
	codexDeviceUserCodeURL              = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL                 = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceVerificationURL          = "https://auth.openai.com/codex/device"
	codexDeviceTokenExchangeRedirectURI = "https://auth.openai.com/deviceauth/callback"
	codexDeviceDefaultIntervalSeconds   = 5
	codexDeviceExpiresInSeconds         = 15 * 60
)

type codexDeviceState struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	CreatedAt    int64  `json:"created_at"`
}

func generateCodexCodeVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}

func generateCodexCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
}

func generateCodexState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}

func codexOAuthCacheKey(sessionID string) string {
	return fmt.Sprintf("codex:oauth:%s", sessionID)
}

func codexDeviceCacheKey(sessionID string) string {
	return fmt.Sprintf("codex:device:%s", sessionID)
}

// StartOAuth creates a PKCE session and returns the authorize URL.
// POST /admin/codex/oauth/start.
func (h *CodexHandlers) StartOAuth(c *gin.Context) {
	ctx := c.Request.Context()

	var req StartCodexOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("invalid request format"))
		return
	}

	state, err := generateCodexState()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to generate oauth state: %w", err))
		return
	}

	codeVerifier, err := generateCodexCodeVerifier()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to generate code verifier: %w", err))
		return
	}

	codeChallenge := generateCodexCodeChallenge(codeVerifier)

	cacheKey := codexOAuthCacheKey(state)
	if err := h.stateCache.Set(ctx, cacheKey, codexOAuthState{CodeVerifier: codeVerifier, CreatedAt: time.Now().Unix()}, xcache.WithExpiration(10*time.Minute)); err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to save oauth state: %w", err))
		return
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", codex.ClientID)
	params.Set("redirect_uri", codex.RedirectURI)
	params.Set("scope", codex.Scopes)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("prompt", "login")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")

	authURL := fmt.Sprintf("%s?%s", codex.AuthorizeURL, params.Encode())

	c.JSON(http.StatusOK, StartCodexOAuthResponse{SessionID: state, AuthURL: authURL})
}

type codexDeviceUserCodeRequest struct {
	ClientID string `json:"client_id"`
}

type codexDeviceUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

type codexDeviceTokenRequest struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
}

type codexDeviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	CodeChallenge     string `json:"code_challenge"`
}

// StartDevice starts the Codex device authentication flow.
// POST /admin/codex/device/start.
func (h *CodexHandlers) StartDevice(c *gin.Context) {
	ctx := c.Request.Context()

	var req StartCodexDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		JSONError(c, http.StatusBadRequest, errors.New("invalid request format"))
		return
	}

	httpClient := h.httpClient
	if req.Proxy != nil {
		httpClient = h.httpClient.WithProxy(req.Proxy)
	}

	var payload codexDeviceUserCodeResponse
	if err := h.doCodexDeviceJSON(ctx, httpClient, codexDeviceUserCodeURL, codexDeviceUserCodeRequest{ClientID: codex.ClientID}, &payload); err != nil {
		JSONError(c, http.StatusBadGateway, err)
		return
	}

	userCode := strings.TrimSpace(payload.UserCode)
	if userCode == "" {
		userCode = strings.TrimSpace(payload.UserCodeAlt)
	}
	deviceAuthID := strings.TrimSpace(payload.DeviceAuthID)
	if userCode == "" || deviceAuthID == "" {
		JSONError(c, http.StatusBadGateway, errors.New("codex device response missing user_code or device_auth_id"))
		return
	}

	sessionID, err := generateCodexState()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to generate device session: %w", err))
		return
	}

	cacheKey := codexDeviceCacheKey(sessionID)
	if err := h.deviceCache.Set(ctx, cacheKey, codexDeviceState{
		DeviceAuthID: deviceAuthID,
		UserCode:     userCode,
		CreatedAt:    time.Now().Unix(),
	}, xcache.WithExpiration(codexDeviceExpiresInSeconds*time.Second)); err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to save device session: %w", err))
		return
	}

	c.JSON(http.StatusOK, StartCodexDeviceResponse{
		SessionID:       sessionID,
		UserCode:        userCode,
		VerificationURI: codexDeviceVerificationURL,
		Interval:        parseCodexDevicePollInterval(payload.Interval),
		ExpiresIn:       codexDeviceExpiresInSeconds,
	})
}

// PollDevice polls the Codex device flow once.
// POST /admin/codex/device/poll.
func (h *CodexHandlers) PollDevice(c *gin.Context) {
	ctx := c.Request.Context()

	var req PollCodexDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("invalid request format"))
		return
	}

	cacheKey := codexDeviceCacheKey(req.SessionID)
	state, err := h.deviceCache.Get(ctx, cacheKey)
	if err != nil || strings.TrimSpace(state.DeviceAuthID) == "" || strings.TrimSpace(state.UserCode) == "" {
		JSONError(c, http.StatusBadRequest, errors.New("invalid or expired device session"))
		return
	}

	httpClient := h.httpClient
	if req.Proxy != nil {
		httpClient = h.httpClient.WithProxy(req.Proxy)
	}

	var tokenResp codexDeviceTokenResponse
	if err := h.doCodexDeviceJSON(ctx, httpClient, codexDeviceTokenURL, codexDeviceTokenRequest{
		DeviceAuthID: state.DeviceAuthID,
		UserCode:     state.UserCode,
	}, &tokenResp); err != nil {
		var httpErr *httpclient.Error
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusForbidden || httpErr.StatusCode == http.StatusNotFound) {
			c.JSON(http.StatusAccepted, PollCodexDeviceResponse{Status: "pending"})
			return
		}
		JSONError(c, http.StatusBadGateway, err)
		return
	}

	authCode := strings.TrimSpace(tokenResp.AuthorizationCode)
	codeVerifier := strings.TrimSpace(tokenResp.CodeVerifier)
	if authCode == "" || codeVerifier == "" {
		JSONError(c, http.StatusBadGateway, errors.New("codex device token response missing authorization_code or code_verifier"))
		return
	}

	tokenProvider := codex.NewTokenProvider(codex.TokenProviderParams{
		HTTPClient: httpClient,
	})
	creds, err := tokenProvider.Exchange(ctx, oauth.ExchangeParams{
		Code:         authCode,
		CodeVerifier: codeVerifier,
		ClientID:     codex.ClientID,
		RedirectURI:  codexDeviceTokenExchangeRedirectURI,
	})
	if err != nil {
		_ = h.deviceCache.Delete(ctx, cacheKey)
		JSONError(c, http.StatusBadGateway, err)
		return
	}

	_ = h.deviceCache.Delete(ctx, cacheKey)
	credentials, err := creds.ToJSON()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to encode credentials: %w", err))
		return
	}

	c.JSON(http.StatusOK, PollCodexDeviceResponse{
		Status:      "success",
		Credentials: credentials,
	})
}

func (h *CodexHandlers) doCodexDeviceJSON(ctx context.Context, client *httpclient.HttpClient, targetURL string, in any, out any) error {
	if client == nil {
		return errors.New("http client is nil")
	}

	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to encode codex device request: %w", err)
	}

	resp, err := client.Do(ctx, &httpclient.Request{
		Method: http.MethodPost,
		URL:    targetURL,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
			"Accept":       []string{"application/json"},
		},
		Body: body,
	})
	if err != nil {
		return err
	}

	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("failed to decode codex device response: %w", err)
	}

	return nil
}

func parseCodexDevicePollInterval(raw json.RawMessage) int {
	if len(raw) == 0 {
		return codexDeviceDefaultIntervalSeconds
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if seconds, err := strconv.Atoi(strings.TrimSpace(asString)); err == nil && seconds > 0 {
			return seconds
		}
	}

	var asInt int
	if err := json.Unmarshal(raw, &asInt); err == nil && asInt > 0 {
		return asInt
	}

	return codexDeviceDefaultIntervalSeconds
}

type ExchangeCodexOAuthRequest struct {
	SessionID   string                  `json:"session_id" binding:"required"`
	CallbackURL string                  `json:"callback_url" binding:"required"`
	Proxy       *httpclient.ProxyConfig `json:"proxy,omitempty"`
}

type ExchangeCodexOAuthResponse struct {
	Credentials string `json:"credentials"`
}

type DecodeCodexAuthJSONRequest struct {
	AuthJSON string `json:"auth_json" binding:"required"`
}

type DecodeCodexAuthJSONResponse struct {
	Credentials string `json:"credentials"`
}

func parseCodexCallbackURL(callbackURL string) (string, string, error) {
	trimmed := strings.TrimSpace(callbackURL)
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return "", "", fmt.Errorf("callback_url must be a full URL")
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("invalid callback_url: %w", err)
	}

	q := u.Query()

	code := q.Get("code")
	if code == "" {
		return "", "", fmt.Errorf("code parameter not found in callback_url")
	}

	state := q.Get("state")
	if state == "" {
		return "", "", fmt.Errorf("state parameter not found in callback_url")
	}

	return code, state, nil
}

// DecodeAuthJSON decodes Codex auth.json into normalized OAuth credentials JSON.
// POST /admin/codex/auth/decode.
func (h *CodexHandlers) DecodeAuthJSON(c *gin.Context) {
	var req DecodeCodexAuthJSONRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("invalid request format"))
		return
	}

	creds, err := codex.DecodeAuthJSON(req.AuthJSON)
	if err != nil {
		JSONError(c, http.StatusBadRequest, fmt.Errorf("failed to decode auth json: %w", err))
		return
	}

	output, err := creds.ToJSON()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to encode credentials: %w", err))
		return
	}

	c.JSON(http.StatusOK, DecodeCodexAuthJSONResponse{Credentials: output})
}

// Exchange exchanges callback URL for OAuth credentials JSON.
// POST /admin/codex/oauth/exchange.
func (h *CodexHandlers) Exchange(c *gin.Context) {
	ctx := c.Request.Context()

	var req ExchangeCodexOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("invalid request format"))
		return
	}

	if req.SessionID == "" || req.CallbackURL == "" {
		JSONError(c, http.StatusBadRequest, errors.New("session_id and callback_url are required"))
		return
	}

	cacheKey := codexOAuthCacheKey(req.SessionID)

	state, err := h.stateCache.Get(ctx, cacheKey)
	if err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("invalid or expired oauth session"))
		return
	}

	if err := h.stateCache.Delete(ctx, cacheKey); err != nil {
		log.Warn(ctx, "failed to delete used oauth state from cache", log.String("session_id", req.SessionID), log.Cause(err))
	}

	code, callbackState, err := parseCodexCallbackURL(req.CallbackURL)
	if err != nil {
		JSONError(c, http.StatusBadRequest, err)
		return
	}

	if callbackState != req.SessionID {
		JSONError(c, http.StatusBadRequest, errors.New("oauth state mismatch"))
		return
	}

	// Create HTTP client with proxy if provided
	httpClient := h.httpClient
	if req.Proxy != nil && req.Proxy.Type == httpclient.ProxyTypeURL && req.Proxy.URL != "" {
		httpClient = h.httpClient.WithProxy(req.Proxy)
	}

	tokenProvider := codex.NewTokenProvider(codex.TokenProviderParams{
		HTTPClient: httpClient,
	})

	creds, err := tokenProvider.Exchange(ctx, oauth.ExchangeParams{
		Code:         code,
		CodeVerifier: state.CodeVerifier,
		ClientID:     codex.ClientID,
		RedirectURI:  codex.RedirectURI,
	})
	if err != nil {
		JSONError(c, http.StatusBadGateway, fmt.Errorf("token exchange failed: %w", err))
		return
	}

	output, err := creds.ToJSON()
	if err != nil {
		JSONError(c, http.StatusInternalServerError, fmt.Errorf("failed to encode credentials: %w", err))
		return
	}

	c.JSON(http.StatusOK, ExchangeCodexOAuthResponse{Credentials: output})
}
