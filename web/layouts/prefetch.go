package layouts

// speculationRules prefetch a same-origin page the pointer rests on
// (nav-latency NL2): `moderate` is Chrome's ~200 ms hover, or the press
// itself, so on real infrastructure the click opens a page already fetched.
// Prefetch only -- prerender would run a page's scripts nobody asked for.
//
// NEVER WHAT COSTS OR ACTS. Sign-in and sign-out; the JSON API; the assets
// (already cached for good); any link whose query carries `tab=`, because a
// load's Preview tab is a billed warehouse query; and anything a template
// marks `data-no-prefetch`. Forms are never prefetched by the browser, which
// keeps every POST -- trigger, toggle, sign-out -- out of it too.
const speculationRules = `{"prefetch":[{"where":{"and":[` +
	`{"href_matches":"/*"},` +
	`{"not":{"href_matches":"/login*"}},` +
	`{"not":{"href_matches":"/logout*"}},` +
	`{"not":{"href_matches":"/api/*"}},` +
	`{"not":{"href_matches":"/assets/*"}},` +
	`{"not":{"href_matches":"/*\\?*tab=*"}},` +
	`{"not":{"selector_matches":"[data-no-prefetch]"}}` +
	`]},"eagerness":"moderate"}]}`
