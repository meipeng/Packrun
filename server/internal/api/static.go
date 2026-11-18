package api

import (
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// staticContentSecurityPolicy 与 deploy/nginx.conf 中的 CSP 保持一致，
// 单二进制直接对外提供页面时也能拿到同等级别的约束。
const staticContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// mountWebUI 在根路径托管内嵌前端，并为 History 路由提供 index.html 回退。
// basePath 是应用对外暴露的子路径（如 /packrun），用于向 index.html 注入 <base>。
func mountWebUI(r *gin.Engine, webFS fs.FS, basePath string) {
	index, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		return
	}
	index = injectBaseHref(index, baseHref(basePath))
	files := http.FileServer(http.FS(webFS))

	r.NoRoute(func(c *gin.Context) {
		reqPath := c.Request.URL.Path
		if isAPIPath(reqPath) {
			notFound(c, "接口不存在")
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}

		clean := strings.TrimPrefix(path.Clean("/"+reqPath), "/")
		if clean != "" && clean != "." {
			if info, statErr := fs.Stat(webFS, clean); statErr == nil && !info.IsDir() {
				if strings.HasPrefix(reqPath, "/assets/") {
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					c.Header("Cache-Control", "no-cache")
				}
				files.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		// 缺失的静态资源不做 SPA 回退，避免把 HTML 当作 JS/CSS 返回。
		if strings.HasPrefix(reqPath, "/assets/") || path.Ext(clean) != "" {
			c.Status(http.StatusNotFound)
			return
		}

		c.Header("Cache-Control", "no-cache")
		c.Header("Content-Security-Policy", staticContentSecurityPolicy)
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/healthz" || strings.HasPrefix(p, "/healthz/")
}

// baseHref 将配置的子路径规范化为以 / 结尾的 href（根路径为 /）。
func baseHref(basePath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" || basePath == "/" {
		return "/"
	}
	return strings.TrimSuffix(basePath, "/") + "/"
}

// injectBaseHref 替换或插入 index.html 的 <base> 标签，使相对资源路径落在子路径下。
func injectBaseHref(index []byte, href string) []byte {
	tag := `<base href="` + html.EscapeString(href) + `" />`
	s := string(index)
	lower := strings.ToLower(s)
	if i := strings.Index(lower, "<base"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j >= 0 {
			return []byte(s[:i] + tag + s[i+j+1:])
		}
	}
	if i := strings.Index(lower, "</head>"); i >= 0 {
		return []byte(s[:i] + tag + s[i:])
	}
	return index
}
