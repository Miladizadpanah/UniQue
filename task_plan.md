# VPN-UI Best Panel — تکمیل نقشه‌ی کار

## وضعیت (audit از کد فعلی)
- ✅ فدراسیون نود (Name+Token، VLESS+Reality، X25519، XTLS Vision، purge زنجیره‌ای) — موجود
- ✅ مودال Manage نود (اتصال/IP خروجی/latency/سرعت/اینبایندهای read-only) — موجود
- ✅ مولتی‌سلیکت گروه کلاینت‌ها — موجود
- ✅ رفع «No inbounds» (fallback listen snapshot) — موجود
- ⚠️ **پریست‌های اینباند**: باگ — پریست «VLESS + TLS» در واقع Reality می‌سازد (`fillPresetDefaults` فقط روی protocol switch می‌کند و security را نادیده می‌گیرد)؛ پریست‌ها بدون فیلد SNI/CDN؛ پورت با bind test انتخاب می‌شود ولی DB-scanned نیست (اسکن DB در `presetInboundPort` هست اما band walk تصادفی ناکارآمد است)
- ⚠️ **مانیتورینگ زنده**: موجود (sparkline + WebSocket + cron 2s) ولی می‌تواند تقویت شود
- ⚠️ **UI گلس‌مورفیسم**: `theme-modern.css` اضافه شده ولی کامل روی همه کلاس‌های ant اعمال نشده

## باگ‌های پریست (findings.md)
1. **VLESS+TLS → Reality می‌سازد**: `fillPresetDefaults` روی `in.Protocol` switch می‌کند ولی `presetVlessSettings("reality")` همیشه با `reality` صدا زده می‌شود. باید preset نام را هم داشته باشد تا تشخیص دهد security = tls است.
2. **Hysteria2 stream ناقص**: `{"security":"none","network":"tcp"}` — باید `network:"hysteria"` + `hysteriaSettings` + TLS (ALPN h3) باشد.
3. **Naive stream ناقص**: `security:"none"` — Naive پروتکل TLS است؛ باید `security:"tls"` + `tlsSettings` باشد.
4. **TUIC stream ناقص**: `{"network":"udp","security":"reality"}` — TUIC QUIC transport خودش را دارد؛ باید `network:"tuic"` + `tuicSettings` باشد.
5. **Shadowsocks 2022 settings blob**: `{"clients":[...]}` — باید `{"method":"2022-blake3-aes-128-gcm","network":"tcp","clients":[...],"server_side":true}` باشد.
6. **VLESS+REALITY realitySettings**: publicKey/shortIds فلات قرار گرفته‌اند؛ باید `settings:{publicKey,fingerprint,spiderX}` ذیلی باشد.

## فازها

### Phase 1: رفع باگ پریست VLESS+TLS + تکمیل stream blobs
- [x] `InboundPreset` ساختار: فیلد `Security` (reality/tls/none) اضافه شود
- [x] `fillPresetDefaults` روی `preset.Security` switch کند
- [x] `presetTlsStream(sni)` ساخته شود
- [x] Hysteria2/TUIC/Naive/Shadowsocks blobs درست‌شوند
- [x] `presetRealityStream` blob درست با settings ذیلی
- [x] `ApplyPresetOverrides` (SNI/CDN/port) + controller + تست‌های blob shape

### Phase 2: فرم پریست در UI (SNI/CDN/Port)
- [x] مودال `applyPreset` با فیلدهای SNI، External Proxy/CDN endpoint، Port
- [x] API `addPreset` فیلدهای جدید را قبول کند
- [x] کلیدهای i18n جدید + knownMissing baseline

### Phase 3: مانیتورینگ زنده — تأیید و تقویت
- [x] چک index.html: sparklineهای cpu/mem/swap/disk/up/down + مودال CPU history + push وضعیت با websocket هر ۲ ثانیه + fallback polling — همه موجود و کامل
- [ ] (تقویت اختیاری) اگر بعدا خواستیم گراف up/down بزرگ‌تر یا گراف RAM اضافه کنیم

### Phase 4: UI گلس‌مورفیسم — تأیید
- [x] چک: `theme-modern.css` روی همه صفحات از `page/head_start` load می‌شود؛ glass/glow/gradient روی `.bo-rail`, `.bo-topbar`, `.bo-card`, `.a-card`, buttons, modals, scrollbar — همه اعمال شده
- [x] ant-bridge.css و components.css مکمل کامل هستند؛ کلاس‌های `.glass` و `.gradient-text` موجود

### Phase 5: build + test + commit
- [x] `GOOS=linux go build ./...` سبز
- [x] تست‌های blob shape پریست (Linux test binary compile)
- [x] چک TOML (quote balance) تمام فایل‌ها
- [x] ارتقا نسخه به ۳.۱.۰ + اپدیت PROJECT_STATUS.md
- [ ] git commit (اگر کاربر خواست)
