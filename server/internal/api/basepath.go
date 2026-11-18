package api

import (
	"net/http"
	"strings"
)

// WithBasePath 将整个服务挂载到子路径（如 /packrun）下。
// 请求 /packrun/api/v1/... 会去掉前缀后交给 next；/packrun 与 / 会重定向到 /packrun/。
// 注意：Nginx 反代必须使用不带 URI 的 proxy_pass（保留 /packrun/ 前缀），
// 否则应用侧看到的路径已被剥离，会与重定向目标不一致。
func WithBasePath(next http.Handler, base string) http.Handler {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" || base == "/" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			// 健康检查保持在根路径，方便负载均衡/监控直接探测。
			next.ServeHTTP(w, r)
			return
		case r.URL.Path == "/", r.URL.Path == base:
			target := base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		case strings.HasPrefix(r.URL.Path, base+"/"):
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, base)
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		default:
			http.NotFound(w, r)
			return
		}
	})
}
