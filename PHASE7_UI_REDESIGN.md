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
- [ ] Phase 7a: design system + index/inbounds/clients
- [ ] Phase 7b: remaining 7 pages
