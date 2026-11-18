package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxJSONBodyBytes 非上传类请求体的上限（1 MiB）。
const maxJSONBodyBytes = 1 << 20

// bodyLimit 限制非上传请求的体积：先按 Content-Length 直接拒绝，再用 MaxBytesReader 兜底。
func bodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isUploadRequest(c) {
			c.Next()
			return
		}
		if c.Request.ContentLength > maxJSONBodyBytes {
			fail(c, http.StatusRequestEntityTooLarge, CodeBadRequest, "请求体过大")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes)
		c.Next()
	}
}

// isUploadRequest 上传接口使用 multipart，体积由包仓库配额控制，不在此处截断。
// 仅按方法与路径判定，避免非上传接口通过伪造 multipart Content-Type 绕过体积限制。
func isUploadRequest(c *gin.Context) bool {
	return c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/v1/packages"
}

// securityHeaders 为所有响应补齐基础安全头，API 响应同时禁止缓存。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Header("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// cors 仅在来源命中白名单时回显 Origin；通配来源不发放凭据。
func cors(allowed []string) gin.HandlerFunc {
	allow := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		allow[o] = true
	}
	wildcard := allow["*"]
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			exact := allow[origin]
			if exact || wildcard {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Add("Vary", "Origin")
				c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-OTP,X-Requested-With")
				c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
				if exact {
					c.Header("Access-Control-Allow-Credentials", "true")
					c.Header("Access-Control-Max-Age", "600")
				}
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
