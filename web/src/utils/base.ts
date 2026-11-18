// appBasePath 返回应用部署路径（始终以 / 结尾）。
// 子路径部署时由服务端在 index.html 中注入 <base href="...">，前端据此推导路由与 API 前缀。
export function appBasePath(): string {
  const href = document.querySelector('base')?.getAttribute('href') || '/'
  return href.endsWith('/') ? href : `${href}/`
}
