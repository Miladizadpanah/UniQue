# Phase 7 — UI Redesign (زیباتر از Sanaei)

## Design Direction

### از Sanaei قوی‌تر
| Sanaei | ما |
|---|---|
| Light-heavy | **Dark-first** (ultra-dark option) |
| Glassmorphism ساده | **Layered glass** (backdrop-filter + noise texture) |
| Accent واحد (teal) | **Dual accent** (teal + indigo) + semantic colors |
| Card مسطح | **Card با depth** (elevation + hover-lift) |
| Motion ساده | **Micro-interactions** (spring easing، stagger، parallax) |
| Tables ساده | **Modern data grid** (row-hover، sticky header، skeleton) |

## Scope

### Phase 7a — Design System + 3 pages اصلی
- `web/assets/css/tokens.css` → v2 (dark-first، dual accent، 3-tier elevation)
- `web/assets/css/components.css` → v2 (modern component set)
- `web/assets/css/custom.css` → v2 (page-specific styles)
- `web/html/index.html` → header + stat cards + traffic chart
- `web/html/inbounds.html` → modern data grid + preset wizard
- `web/html/clients.html` → modern table + bulk ops

### Phase 7b — 7 pages باقی‌مانده
- `web/html/nodes.html` — simpler node dashboard
- `web/html/tunnels.html` — card-based tunnel wizard
- `web/html/settings.html` — section-based settings
- `web/html/xray.html` — xray dashboard
- `web/html/admins.html` — simple admin table
- `web/html/resellers.html` — reseller table
- `web/html/core.html` — core status dashboard

## Test Plan
- `go build ./...` + `go vet ./...` سبز
- CI test سبز
- Browser test: هر page render شود بدون error
- اطمینان که i18n کلیدها در همه pages کار می‌کند

## Status

Release: **2.8.0** (`config/version`, tag `v2.8.0`, asset `vpn-ui-amd64`).

- [x] Phase 7a: design system (tokens + components restored, no orphan `bo-*`
      class left unstyled) + index / inbounds / clients
- [x] Phase 7b: nodes and tunnels rebuilt as card grids; settings, xray,
      admins, resellers and core on the same shell
- [x] Guards that would have caught this phase's regressions:
      `web/i18n_toml_test.go` (real TOML, template-key coverage, mojibake),
      `web/page_render_test.go` (every page executes),
      `web/page_outside_app_test.go` + `web/htmlscan` (a dialog outside `#app`
      is never compiled — the clone-to-node bug), `web/assets_css_test.go` +
      `web/csscheck` (a template class no stylesheet defines)
- [x] Clone-to-node: an inbound's menu clones it onto a registered node, and
      the cloned accounts keep the hub client's `subId`, so their configs and
      share links are served by the same `/sub/<subId>` as local accounts

