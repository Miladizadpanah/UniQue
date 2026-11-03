# Findings — VPN-UI (UniQue)

## کدبیس
- فوِیک 3x-ui با Go + Gin + Vue 2 (CDN) + Ant Design Vue
- `web/html/*.html` — template'های Go (Go text/template) + inline Vue
- `web/service/*.go` — سرویس‌ها؛ `web/controller/*.go` — endpointهای API
- `web/assets/js/model/inbound.js` — مدل JS inbound (blob shape مرجع)

## پریست اینباند (web/service/inbound_presets.go)
- ۶ پریست: VLESS+REALITY, Hysteria2, VLESS+TLS, TUIC v5, Naive, Shadowsocks 2022
- **باگ ۱**: `fillPresetDefaults` فقط روی `in.Protocol` switch می‌کند → هر دو پریست VLESS دقیقاً همین blob را می‌سازند: Settings=reality-accounts, Stream=presetRealityStream(). یعنی «VLESS + TLS» Reality می‌سازد، نه TLS!
- **باگ ۲**: Hysteria2 stream `{"security":"none","network":"tcp"}` است — ولی Hysteria2 باید `network:"hysteria"` + `hysteriaSettings` + TLS (ALPN h3,سری‌بندی) داشته باشد (مطابق `HysteriaStreamSettings` در inbound.js و `genHysteriaLink` در sub/subService.go که از `stream["tlsSettings"]` ALPN/SNI می‌خواند)
- **باگ ۳**: NaiveProxy `security:"none"` است — Naive TLS پروتکل است؛ باید `security:"tls"` داشته باشد (comment stream_settings.html: «anytls and naive TLS نیستند optional… security='tls'»)
- **باگ ۴**: TUIC stream `{"network":"udp","security":"reality"}` است — ولی TUIC QUIC transport خود را دارد (`network:"tuic"` + `tuicSettings`)؛ Reality روی UDP/QUIC نیست (comment inbound.js: «tuic is QUIC, which REALITY does not cover»)
- **باگ ۵**: Shadowsocks settings blob `{"clients":[...]}` است؛ پروتکل SS blob صحیح `{"servers":[{"method":"2022-blake3-aes-128-gcm",...}]}` + `clients` است
- **باگ ۶**: پریست VLESS+REALITY stream: `realitySettings` blob فرم پنل، `publicKey` را در `settings` ذیلی نگه می‌دارد (reality_settings.html + `RealityStreamSettings.toJson`); blob فعلی پریست `publicKey` را فلات نگه می‌دارد — باید فرم درست `{"dest":...,"serverNames":[...],"settings":{"fingerprint":"chrome","publicKey":"..."},"shortIds":[...],"privateKey":"..."}` باشد (fedRealityStream در inbound.go هم publicKey را فلات نگه داشته — این در Xray واقعاً اشتباه است؛ Xray publicKey را در `reality.settings.publicKey` می‌خواهد. باید check شود چه چیزی را Xray قبول می‌کند)

## blob shape مرجع (web/assets/js/model/inbound.js)
- `StreamSettings.toJson()`: network, security, tlsSettings (aگر isTls), realitySettings (اگر isReality), tcpSettings, wsSettings...
- `TlsStreamSettings`: sni, minVersion/maxVersion, alpn, certificates[], settings{fingerprint,...}
- `RealityStreamSettings`: target, serverNames (comma string in form), privateKey, shortIds, settings{publicKey, fingerprint, spiderX}
- `HysteriaStreamSettings`: protocol/version/auth/udpIdleTimeout/masquerade → کلید `hysteriaSettings`
- VLESS client entry: email, enable, id, flow, encryption
- Hysteria2 client entry: {id: email, auth, enable} (طبق presetHysteriaSettings + genHysteriaLink که `clients[clientIndex].Auth` را می‌خواند)
- TUIC client entry: طبق comment در inbound.js «TUIC account BOTH uuid AND password دار است» → {id: email, uuid, password, congestion, enable}

## مانیتورینگ زنده
- `web/controller/server.go`: cron `@every 2s` → `refreshStatus()` → `GetStatus` + `AppendCpuSample` + `websocket.BroadcastStatus`
- `web/html/index.html`: sparkline component (CPU/mem/swap/disk/up/down) + CPU history modal (`/panel/api/server/cpuHistory/:bucket`) + websocket `status` event → `setStatus` → `pushHistory`
- یعنی مانیتورینگ زنده وجود دارد؛ تقویت = تأیید + اگر چیزی ناقص باشد اضافه کردن

## UI گلس‌مورفیسم
- `theme-modern.css` (459 خط): glass variables، gradient brand، card hover-lift، modal blur، scrollbar، utility classهای `.glass`, `.gradient-text`
- همه صفحات از `page/head_start` ← theme-modern.css load می‌کنند ✓
- چک لازم: آیا pages اصلی (index/inbounds/clients/nodes) از classهای glass استفاده می‌کنند؟

## i18n
- کلیدهای `inboundPresets`/`applyPreset` در en_US هست (lines 532-533) — parity test: `web/i18n_toml_test.go`؛ اگه کلید جدید اضافه می‌کنم باید توی همه toml‌ها باشد

## کنترلر پریست (web/controller/inbound.go)
- `POST /panel/api/inbounds/addPreset`: PostForm preset + remark فقط
- `GET /panel/api/inbounds/presets`: لیست

## درگ‌ها/محدودیت‌ها
- Build در Windows: `go build` ممکن است بابت syscall linux-only fail کند (NOTES: "Tests blocked on Windows")
- Template parse test: وجود دارد (i18n_toml_test + template parse check)
