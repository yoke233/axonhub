package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"github.com/looplj/axonhub/internal/build"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/server/assets"
	"github.com/looplj/axonhub/internal/server/biz"
)

type SystemHandlersParams struct {
	fx.In

	SystemService *biz.SystemService
}

func NewSystemHandlers(params SystemHandlersParams) *SystemHandlers {
	return &SystemHandlers{
		SystemService: params.SystemService,
	}
}

type SystemHandlers struct {
	SystemService *biz.SystemService
}

// SystemStatusResponse 系统状态响应.
type SystemStatusResponse struct {
	IsInitialized bool `json:"isInitialized"`
}

// HealthResponse 健康检查响应.
type HealthResponse struct {
	Status    string     `json:"status"`
	Timestamp time.Time  `json:"timestamp"`
	Version   string     `json:"version"`
	Build     build.Info `json:"build"`
	Uptime    string     `json:"uptime"`
}

// InitializeSystemRequest 系统初始化请求.
type InitializeSystemRequest struct {
	OwnerEmail     string `json:"ownerEmail"     binding:"required,email"`
	OwnerPassword  string `json:"ownerPassword"  binding:"required,min=6"`
	OwnerFirstName string `json:"ownerFirstName" binding:"required"`
	OwnerLastName  string `json:"ownerLastName"  binding:"required"`
	BrandName      string `json:"brandName"      binding:"required"`
	PreferLanguage string `json:"preferLanguage,omitempty"`
}

// InitializeSystemResponse 系统初始化响应.
type InitializeSystemResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type WebhookDebugResponse struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   map[string][]string `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    json.RawMessage     `json:"body"`
}

// GetSystemStatus returns the system initialization status.
func (h *SystemHandlers) GetSystemStatus(c *gin.Context) {
	isInitialized, err := h.SystemService.IsInitialized(c.Request.Context())
	if err != nil {
		JSONError(c, http.StatusInternalServerError, errors.New("Failed to check system status"))
		return
	}

	c.JSON(http.StatusOK, SystemStatusResponse{
		IsInitialized: isInitialized,
	})
}

// Health returns the application health status and build information.
func (h *SystemHandlers) Health(c *gin.Context) {
	buildInfo := build.GetBuildInfo()

	c.JSON(http.StatusOK, HealthResponse{
		Status:    "healthy",
		Timestamp: time.Now(),
		Version:   build.Version,
		Build:     buildInfo,
		Uptime:    buildInfo.Uptime,
	})
}

// drainFilePath returns the path of the drain marker file. When this file
// exists, the instance reports itself as not ready (see Readiness) so the
// load balancer stops routing new traffic to it while in-flight requests are
// allowed to finish. The file is created by the deployment pre-stop hook
// during a zero-downtime rollout. Override with AXONHUB_DRAIN_FILE.
func drainFilePath() string {
	if p := os.Getenv("AXONHUB_DRAIN_FILE"); p != "" {
		return p
	}

	return "/tmp/drain"
}

// Readiness reports whether the instance is ready to receive new traffic.
// Unlike Health (a liveness probe that is always 200 while the process is up),
// Readiness returns 503 once the drain marker file exists. This lets an
// active load-balancer health check (e.g. Traefik) remove the instance from
// rotation at the start of a graceful drain, after which the pre-stop hook's
// sleep window allows in-flight requests to complete before shutdown.
func (h *SystemHandlers) Readiness(c *gin.Context) {
	if _, err := os.Stat(drainFilePath()); err == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":    "draining",
			"timestamp": time.Now(),
		})

		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "ready",
		"timestamp": time.Now(),
	})
}

// WebhookEcho echos inbound webhook requests for validation.
func (h *SystemHandlers) WebhookEcho(c *gin.Context) {
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("failed to read request body"))
		return
	}

	resp := WebhookDebugResponse{
		Method:  c.Request.Method,
		Path:    c.Request.URL.Path,
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
		Body:    json.RawMessage(bodyBytes),
	}

	log.Info(c.Request.Context(), "received webhook debug request",
		log.String("method", resp.Method),
		log.String("path", resp.Path),
		log.Any("query", resp.Query),
		log.Any("headers", resp.Headers),
		log.Any("body", resp.Body),
	)

	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_ = json.NewEncoder(c.Writer).Encode(resp)
}

// InitializeSystem initializes the system with owner credentials.
func (h *SystemHandlers) InitializeSystem(c *gin.Context) {
	var req InitializeSystemRequest

	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, InitializeSystemResponse{
			Success: false,
			Message: "Invalid request format",
		})

		return
	}

	// Check if system is already initialized
	isInitialized, err := h.SystemService.IsInitialized(c.Request.Context())
	if err != nil {
		JSONError(c, http.StatusInternalServerError, errors.New("Failed to check initialization status"))
		return
	}

	if isInitialized {
		c.JSON(http.StatusBadRequest, InitializeSystemResponse{
			Success: false,
			Message: "System is already initialized",
		})

		return
	}

	// Initialize system
	err = h.SystemService.Initialize(c.Request.Context(), &biz.InitializeSystemParams{
		OwnerEmail:     req.OwnerEmail,
		OwnerPassword:  req.OwnerPassword,
		OwnerFirstName: req.OwnerFirstName,
		OwnerLastName:  req.OwnerLastName,
		BrandName:      req.BrandName,
		PreferLanguage: req.PreferLanguage,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, InitializeSystemResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to initialize system: %v", err),
		})

		return
	}

	c.JSON(http.StatusOK, InitializeSystemResponse{
		Success: true,
		Message: "System initialized successfully",
	})
}

// GetFavicon returns the system brand logo as favicon.
func (h *SystemHandlers) GetFavicon(c *gin.Context) {
	ctx := c.Request.Context()

	brandLogo, err := h.SystemService.BrandLogo(ctx)
	if err != nil {
		log.Error(ctx, "Failed to get brand logo", log.Cause(err))
	}

	// 如果没有设置品牌标识，返回默认 favicon
	if brandLogo == "" {
		defaultFaviconData, err := assets.Favicon.ReadFile("favicon.ico")
		if err != nil {
			JSONError(c, http.StatusInternalServerError, errors.New("Failed to read default favicon"))
			return
		}

		c.Header("Content-Type", "image/x-icon")
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "image/x-icon", defaultFaviconData)

		return
	}

	// 解析 base64 编码的图片数据
	// 假设格式为 "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA..."
	if !strings.HasPrefix(brandLogo, "data:") {
		JSONError(c, http.StatusBadRequest, errors.New("Invalid brand logo format"))
		return
	}

	// 提取 MIME 类型和 base64 数据
	parts := strings.Split(brandLogo, ",")
	if len(parts) != 2 {
		JSONError(c, http.StatusBadRequest, errors.New("Invalid brand logo format"))
		return
	}

	// 提取 MIME 类型
	headerPart := parts[0] // "data:image/png;base64"
	mimeStart := strings.Index(headerPart, ":")

	mimeEnd := strings.Index(headerPart, ";")
	if mimeStart == -1 || mimeEnd == -1 {
		JSONError(c, http.StatusBadRequest, errors.New("Invalid brand logo format"))
		return
	}

	mimeType := headerPart[mimeStart+1 : mimeEnd]

	// 解码 base64 数据
	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		JSONError(c, http.StatusBadRequest, errors.New("Failed to decode brand logo"))
		return
	}

	// 设置响应头
	c.Header("Content-Type", mimeType)
	c.Header("Cache-Control", "public, max-age=3600") // 缓存 1 小时
	c.Header("Content-Length", fmt.Sprintf("%d", len(imageData)))

	// 返回图片数据
	c.Data(http.StatusOK, mimeType, imageData)
}
