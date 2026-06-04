/**
 * Same-origin redirect gate for `?from=`-style post-login destinations.
 *
 * The login + auth.callback pages bounce the user to whatever URL was
 * stashed in `?from=` before the OAuth round-trip. Without a check, an
 * attacker can craft `https://quantum-atlas.ai/login?from=//evil.com`
 * and use our real domain + real GitHub OAuth as a phishing launchpad:
 * the user sees a legitimate sign-in, then `window.location.assign` /
 * `replace` lands them on evil.com after a fresh `Set-Cookie`.
 *
 * The "library" used here is the browser's WHATWG URL parser — there is
 * no more authoritative implementation. Wrappers like `is-absolute-url`
 * or `url-parse` either delegate to URL anyway (no value-add) or roll
 * their own parser and have historically lagged behind the spec on
 * exactly the attack shapes that matter (e.g. backslash normalisation,
 * IDN homoglyphs).
 *
 * ## Rejected shapes (return fallback)
 *   "//evil.com/x"        — protocol-relative reference, inherits https:
 *   "/\\evil.com/x"       — backslash; URL parser normalises to "/" before host
 *   "https://evil.com/x"  — absolute, cross-origin
 *   "javascript:alert()"  — non-http(s) scheme
 *   ""                    — empty
 *   "/login"              — loop-back to login (would re-enter the bounce)
 *
 * ## Accepted shapes (returned verbatim)
 *   "/"                   — site root
 *   "/wiki/foo"           — same-origin path
 *   "/foo?bar=1"          — same-origin path + query (CLI loopback uses this)
 *   "/foo#anchor"         — same-origin path + fragment
 *
 * The cheap structural pre-check is intentional duplication of the URL
 * check: it short-circuits the most common attack inputs without a
 * heap allocation, AND documents the two shapes (`//host`, `/\host`)
 * a reader of this file most needs to know about.
 */
export function safeRedirect(
  dest: string | undefined | null,
  fallback = '/',
): string {
  if (!dest || dest === '/login') return fallback
  // Must be a relative URL starting with exactly one '/'. Reject
  // protocol-relative ("//host") and backslash variants ("/\host")
  // up front — those are the two shapes the URL parser would silently
  // resolve to a cross-origin host inheriting our scheme.
  if (!dest.startsWith('/') || dest.startsWith('//') || dest.startsWith('/\\')) {
    return fallback
  }
  // Authoritative same-origin check via the browser URL parser.
  // window.location.origin is the only origin we will ever navigate to
  // post-login; resolving against it and asserting the parsed origin
  // is identical catches anything the structural check missed
  // (encoded characters, IDN, etc.).
  try {
    const url = new URL(dest, window.location.origin)
    if (url.origin !== window.location.origin) return fallback
    return dest
  } catch {
    return fallback
  }
}
