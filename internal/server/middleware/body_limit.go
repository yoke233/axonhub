package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

func MaxRequestBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit <= 0 || c.Request == nil {
			c.Next()
			return
		}

		if c.Request.ContentLength > limit {
			AbortWithError(c, http.StatusRequestEntityTooLarge, fmt.Errorf("request body too large: limit is %d bytes", limit))
			return
		}

		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}

		c.Next()
	}
}
