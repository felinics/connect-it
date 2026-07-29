// Thin wrapper over window.location: an OAuth redirect must leave the SPA as
// a full page navigation, and tests mock this module.
export function redirectTo(url: string) {
  window.location.assign(url)
}
