package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	timeoutDurationKey = "axonhub.request_timeout.duration"
	timeoutDeadlineKey = "axonhub.request_timeout.deadline"
)

func WithTimeout(ts time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), ts)
		defer cancel()

		if deadline, ok := ctx.Deadline(); ok {
			c.Set(timeoutDeadlineKey, deadline)
		}
		c.Set(timeoutDurationKey, ts)

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
