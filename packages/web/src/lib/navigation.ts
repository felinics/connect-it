// window.location 的薄封装：OAuth 跳转要整页离开 SPA，测试里 mock 这里。
export function redirectTo(url: string) {
  window.location.assign(url)
}
