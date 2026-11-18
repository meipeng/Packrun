package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 错误码分段：1xxx 认证 / 2xxx 权限 / 3xxx 参数与资源 / 4xxx 部署执行 / 5xxx 系统
const (
	CodeOK            = 0
	CodeAuthFailed    = 1001
	CodeTokenInvalid  = 1002
	CodeOTPRequired   = 1428
	CodeOTPInvalid    = 1003
	CodeForbidden     = 2001
	CodeBadRequest    = 3001
	CodeNotFound      = 3002
	CodeConflict      = 3003
	CodeDeployFailed  = 4001
	CodeInternalError = 5001
)

type body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, body{Code: CodeOK, Message: "ok", Data: data})
}

func fail(c *gin.Context, status, code int, msg string) {
	c.AbortWithStatusJSON(status, body{Code: code, Message: msg})
}

func badRequest(c *gin.Context, msg string) {
	fail(c, http.StatusBadRequest, CodeBadRequest, msg)
}

func notFound(c *gin.Context, msg string) {
	fail(c, http.StatusNotFound, CodeNotFound, msg)
}

func forbidden(c *gin.Context, msg string) {
	fail(c, http.StatusForbidden, CodeForbidden, msg)
}

func internal(c *gin.Context, msg string) {
	fail(c, http.StatusInternalServerError, CodeInternalError, msg)
}
