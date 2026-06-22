package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/looplj/axonhub/internal/contexts"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/pkg/xcontext"
	"github.com/looplj/axonhub/internal/tracing"
)

// AccessLog returns a middleware that logs access information for each request.
// It logs: status code, method, path, graphql operation (if applicable), and errors.
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		ctx := c.Request.Context()

		// Collect errors from gin context and request context
		var errMsgs []string
		for _, e := range c.Errors {
			errMsgs = append(errMsgs, e.Error())
		}

		for _, e := range contexts.GetErrors(ctx) {
			errMsgs = append(errMsgs, e.Error())
		}

		// Only log if there are errors or status >= 400
		status := c.Writer.Status()
		if status < 400 && len(errMsgs) == 0 {
			return
		}

		latency := time.Since(start)

		fields := []log.Field{
			log.Int("status", status),
			log.String("method", c.Request.Method),
			log.String("path", c.Request.URL.Path),
			log.Duration("latency", latency),
			log.String("client_ip", c.ClientIP()),
		}
		fields = append(fields, requestContextDiagnosticFields(ctx, time.Now())...)
		if timeoutValue, ok := c.Get(timeoutDurationKey); ok {
			if timeout, ok := timeoutValue.(time.Duration); ok {
				fields = append(fields, log.Duration("route_timeout", timeout))
			}
		}
		if deadlineValue, ok := c.Get(timeoutDeadlineKey); ok {
			if deadline, ok := deadlineValue.(time.Time); ok {
				fields = append(fields, log.Time("route_timeout_deadline", deadline))
			}
		}

		// Add GraphQL operation name if available
		if opName, ok := tracing.GetOperationName(ctx); ok {
			fields = append(fields, log.String("operation", opName))
		}

		// Add errors if present
		if len(errMsgs) > 0 {
			fields = append(fields, log.Strings("errors", errMsgs))
		}

		// The route timeout middleware cancels the request context after the
		// handler returns. Log with WithoutCancel so the generic context hook does
		// not add a misleading context_error=context canceled to normal errors.
		log.Error(context.WithoutCancel(ctx), "[ACCESS]", fields...)
	}
}

func requestContextDiagnosticFields(ctx context.Context, now time.Time) []log.Field {
	diag := xcontext.Inspect(ctx, now)

	fields := []log.Field{
		log.String("request_context_state", diag.State),
		log.String("request_context_observed_at", "access_log_after_handler"),
	}
	if diag.Err != nil {
		fields = append(fields, log.NamedError("request_context_error", diag.Err))
	}
	if diag.HasDeadline {
		fields = append(fields,
			log.Time("request_context_deadline", diag.Deadline),
			log.Int64("request_context_remaining_ms", diag.Remaining.Milliseconds()),
		)
	}

	return fields
}
