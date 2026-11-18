// Package webui 提供构建时嵌入的前端静态资源。
//
// 源码仓库中的 dist/ 只保留 .gitkeep；执行 `make build`（仓库根目录）会先构建
// web/ 并把产物同步到 dist/，再由 go:embed 打进 packrund 二进制，实现单文件交付。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS 返回内嵌的前端文件系统；未嵌入构建产物（例如只构建后端）时 ok 为 false。
func FS() (fs.FS, bool) {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return nil, false
	}
	return dist, true
}
