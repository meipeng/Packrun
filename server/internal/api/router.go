package api

import (
	"log"

	"github.com/gin-gonic/gin"

	"github.com/packrun/packrun/internal/model"
	"github.com/packrun/packrun/internal/webui"
)

func NewRouter(app *App) *gin.Engine {
	r := gin.New()
	// 仅采信配置中列出的可信代理，其余请求一律以 TCP 对端地址为准。
	if err := r.SetTrustedProxies(app.Cfg.Server.TrustedProxies); err != nil {
		log.Printf("设置可信代理失败，将不信任任何转发头: %v", err)
	}
	r.Use(gin.Logger(), gin.Recovery(), securityHeaders(), bodyLimit(), cors(app.Cfg.Server.AllowedOrigins))
	r.MaxMultipartMemory = 32 << 20

	r.GET("/healthz", app.healthz)

	v1 := r.Group("/api/v1")
	v1.POST("/auth/login", app.login)

	authed := v1.Group("")
	authed.Use(app.authRequired())
	{
		authed.GET("/auth/me", app.me)
		authed.POST("/auth/password", app.changePassword)
		authed.POST("/auth/otp/setup", app.otpSetup)
		authed.POST("/auth/otp/activate", app.otpActivate)
		authed.POST("/auth/otp/rebind", app.otpRebind)
		authed.POST("/auth/otp/disable", app.otpDisable)
		authed.GET("/auth/recovery-codes", app.recoveryCodeStatus)
		authed.POST("/auth/recovery-codes/regenerate", app.regenerateRecoveryCodes)

		authed.GET("/overview", app.overview)

		authed.GET("/projects", app.listProjects)
		authed.POST("/projects", app.createProject)
		authed.GET("/projects/:id", app.getProject)
		authed.PUT("/projects/:id", app.updateProject)
		authed.DELETE("/projects/:id", app.deleteProject)
		authed.GET("/projects/:id/history", app.projectHistory)
		authed.POST("/projects/:id/publish", app.publishProject)
		authed.POST("/projects/:id/rollback", app.rollbackProject)

		authed.GET("/servers", app.listServers)
		authed.GET("/servers/:id", app.getServer)
		authed.POST("/servers/:id/ping", app.pingServer)
		authed.POST("/servers/:id/probe", app.probeServer)
		authed.POST("/servers/:id/hostkey/probe", app.probeServerHostKey)

		authed.GET("/directories", app.listDirectories)

		authed.GET("/scripts", app.listScripts)

		authed.GET("/packages", app.listPackages)
		authed.POST("/packages", app.uploadPackage)
		authed.DELETE("/packages/:id", app.deletePackage)

		authed.GET("/tasks", app.listTasks)
		authed.GET("/tasks/:id", app.getTask)
		authed.POST("/tasks", app.createTask)
		authed.POST("/tasks/:id/cancel", app.cancelTask)
		authed.DELETE("/tasks/:id", app.deleteTask)
	}

	// 管理员：配置类写操作
	admin := authed.Group("")
	admin.Use(requireRoles(model.RoleAdmin))
	{
		admin.POST("/servers", app.createServer)
		admin.PUT("/servers/:id", app.updateServer)
		admin.DELETE("/servers/:id", app.deleteServer)
		admin.DELETE("/servers/:id/hostkey", app.clearServerHostKey)

		admin.POST("/directories", app.createDirectory)
		admin.PUT("/directories/:id", app.updateDirectory)
		admin.DELETE("/directories/:id", app.deleteDirectory)

		admin.POST("/scripts", app.createScript)
		admin.PUT("/scripts/:id", app.updateScript)
		admin.DELETE("/scripts/:id", app.deleteScript)

		admin.GET("/users", app.listUsers)
		admin.POST("/users", app.createUser)
		admin.PUT("/users/:id", app.updateUser)
		admin.DELETE("/users/:id", app.deleteUser)
		admin.POST("/users/:id/otp/reset", app.resetUserOTP)
	}

	// 审计：管理员与审计员
	auditGroup := authed.Group("")
	auditGroup.Use(requireRoles(model.RoleAdmin, model.RoleAuditor))
	{
		auditGroup.GET("/audit", app.listAuditLogs)
	}

	// 内嵌前端存在时由本进程直接托管，实现单二进制部署；未构建前端时保持纯 API 行为。
	if webFS, ok := webui.FS(); ok {
		mountWebUI(r, webFS, app.Cfg.Server.BasePath)
	}

	return r
}
