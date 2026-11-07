# Phase 7 — UI Redesign (زیباتر از Sanaei) → **v2.9.0**

## Version
- `config/version` → `2.9.0`
- GitHub tag → `v2.9.0`
- Release → `v2.9.0` با asset `vpn-ui-amd64`

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
- [x] Phase 7a step 1: tokens.css v2 (dark-first + dual accent) — commit 7df8885
- [x] Phase 7a step 2: components.css v2 (modern component set) — WIP
- [ ] Phase 7a step 3: rebuild index.html (stat cards + charts)
- [ ] Phase 7a step 4: rebuild inbounds.html (modern data grid + preset wizard)
- [ ] Phase 7a step 5: rebuild clients.html (modern table + bulk ops)
- [ ] Phase 7b: 7 remaining pages (nodes, tunnels, settings, xray, admins, resellers, core)
- [ ] Bump config/version to 2.9.0
- [ ] Tag v2.9.0 + push → CI build + publish
