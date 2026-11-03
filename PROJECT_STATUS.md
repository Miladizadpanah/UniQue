# VPN-UI (UniQue) — گزارش کامل وضعیت پروژه

> آخرین به‌روزرسانی: ۲ نوامبر ۲۰۲۶ · نسخه‌ی جاری: **v3.0.1** (فاز ۱ + ۲ + ۲b + ۳ + ۴ + ۵ + ۶)
> ریپازیتوری دپلوی: `github.com/Miladizadpanah/UniQue`

---

## ۰. فاز ۲ — تونل بومی سبک (BackPack-style) [کامیت c6055af]

مطابق نقشه‌راه؛ به‌جای ادغام مستقیم کد ۵۰هزارسطری BackPack (با وابستگی‌های
quic-go/kcp/PCK/EBPF و لایسنس AGPL)، یک موتور تونل **بومی سبک** با همان
قابلیت‌ها در پنل ساخته شد:

- **مدل `TunnelPair`** روی جدول `tunnel_pairs` (جدا از `TunnelMesh` قدیمی
  که روی جدول `tunnels` است). فیلدها: Role (server/client)، Transport
  (`tcp|tcp-tls|ws|wss|udp`)، FallbackTransports، ServerAddr، ForwardedPorts،
  Token (۶۴-هگز)، TLSFingerprint، Tuning، ProxyProtoV2 + گیج‌های زنده
  (Status/LastError/Connections/UpSince/BytesUp/Down).
- **موتور** (`web/service/tunnel`): ۵ کالر سبک stdlib — TCP، TLS 1.3/TCP،
  WebSocket، WSS، UDP-datelgram. کانال control با header نسخه‌دار + توکن
  (مقایسه constant-time + چک نقش مخالف). keepalive هر ۱۵ ثانیه تا تونل بی‌صدا
  «stalled» قلمداد نشود. کالرهای سنگین KCP/QUIC/Noise/ICMP و L3-GRE فاز
  بعد.
- **Service**: CRUD + **link-test** (دایل + latency + ترنسپورت پیشنهادی برای
  مسیر سنجش‌شده). Save روی ردیف جدید token می‌سازد؛ روی ویرایش token زنده را
  دست نمی‌زند (باگ «تونلم بعد از save مرده» دقیقاً همین است).
- **Controller + UI**: `/panel/tunnels` + `/panel/api/tunnels` + صفحه‌ی
  `tunnels.html` با wizard (نقش + ترنسپورت + tuning + peer + token + پورت‌ها +
  fingerprint + PROXY-v2) + دایالوگ نتیجه‌ی link-test. ورودی در سایدبار. i18n
  en_US + fa_IR.
- **Watchdog** (`web/job/tunnel_watchdog_job.go`): هر ۳۰ ثانیه، تونلی که آخرین
  بایتش از دو keepalive (۳۰ ثانیه ساکتی روی لینکی که هر ۱۵ث پینگ می‌کند)
  گذسته → `stalled`. یک بلپ گذرا تونل را **نمی‌کشد**؛ دکمه‌ی disable اپراتور
  teardown صریح است.
- **Boot**: `StartAll` + watchdog در `web/web.go`.
- **Build**: `GOOS=linux go build ./...` + `go vet` سبز.

### وضعیت فازها
- ✅ فاز ۱ — نود قوی (heartbeat + آپدیت از هاب) [کامیت 965ead2]
- ✅ فاز ۲ — تونل بومی سبک [کامیت c6055af]
- ✅ فاز ۲b — ری‌لای ترافیک واقعی کاربر روی کانال control تونل [کامیت 0578c83]
- ✅ فاز ۳ — صفحه‌ی 3X-UI (app icons، balancers، custom template، external import) [کامیت b5aeeac]
- ✅ فاز ۴ — inbound clone to nodes + mTLS + tunnel health monitor + CPU/mem history [کامیت 2d694cb]
- ✅ فاز ۵ — control-plane NX-style (2FA TOTP، session mgmt، webhooks outbox، backup encryption، Ctrl-K palette، PWA) [کامیت 3413edf]
- ✅ فاز ۶ — fallbacks + MTProto quota + calendar renewal + API scoped tokens [کامیت eccb100]


## ۰f. فاز ۶ — fallbacks + MTProto quota + calendar renewal + API scoped tokens

چهار ارتقا نهایی که پنل را به «بهترین پنل» تبدیل می‌کند:

- **Fallback transports (توانایی تغییر کالر زنده‌ی تونل)**: موتور تونل حالا وقتی کالر اصلی (active transport) settle نشود، به‌صورت خودکار fallback‌های به‌ترتیب را امتحان می‌کند (`openTransportWithFallbacks` در `web/service/tunnel/core.go`). رول client روی هر retry، زنجیره‌ی (active + fallbacks) را walk می‌کند؛ هر کالر که dial بشود، برمی‌گردد و `settledOnCarrier` + `settledOn` روی handle ثبت می‌شود. `Manager.SettledOnCarrier(id)` accessor جدید. Watchdog (`web/job/tunnel_watchdog_job.go`) حالا روی «بازگشت به up» اگر تونل روی یک fallback کالر settle شده باشد، event سلامت «recovered on fallback carrier X» ثبت می‌کند. UI تونل: فیلد چندمانتاکی «Fallback transports» در wizard (`web/html/tunnels.html`) + i18n `pages.tunnels.fallbacks`/`fallbacksPlaceholder`.

- **MTProto per-client byte quota**: `mtprotoClient.DataQuotaBytes int64` (JSON `dataQuotaBytes`) به `web/service/mtproto.go` اضافه شد؛ `buildServerConfig` حالا پارامتر سومی `inboundTotalGB int64` می‌گیرد (Total اینباند) و بخش جدید `[access.user_data_limit]` به‌ازای هر client render می‌کند؛ helper `resolveEffectiveQuota(inboundTotal, accountTotal)` قواعد 0-loses را اجرا می‌کند (همان شکل `resolveUserLimitOverride`). هر دو call site (`web/service/coreconfig.go:552` و `web/service/mtproto.go generateServerConfig`) با `inbound.Total` به‌روز شدند؛ ۴ test mtproto هم (`buildServerConfig(inbound, settings, inbound.Total)`) patch شدند. JS/model: `MtprotoUser` حالا `dataQuotaBytes` (+ `_dataQuotaGB` getter/setter) دارد (`web/assets/js/model/inbound.js`)؛ مودال membership (`web/html/modals/client_membership_modal.html`) input `dataQuota` (GB) فقط برای mtproto + `isMtproto` getter + post `dataQuotaBytes` (bytes).

- **Calendar renewal (تمدید دستی)**: `InboundService.RenewClientByEmail(clientEmail)` در `web/service/inbound.go` — پریود reset حساب را می‌خواند، `ExpiryTime` را دقیقاً مثل `autoRenewClients` به‌ جلو می‌راند، سپس `ResetClientExpiryTimeByEmail` + update DB روی ردیف traffic (expiry_time + enable) را صدا می‌زند. مسیر جدید `POST /:id/renewClient/:email` در `web/controller/inbound.go` (gated با `PermEditClient` + owns + ownsClient، مثل `resetClientTraffic`). `web/html/clients.html` یک آیتم `renew` (آیکون کالندر) در منوی row + روش `renewAccount(row)` + i18n `pages.inbounds.renewClient`/`toasts.renewClientSuccess` (fa: «تمدید الان»).

- **Scoped API tokens**: `model.ApiToken` (جدول `api_tokens`) + scope bitmask consts (`ScopeNodeRead`/`ScopeNodeWrite`/`ScopeHeartbeat`) + `model.AllApiTokenScopes` در `database/model/tunnel.go`؛ در `database/db.go` AutoMigrate ثبت شد. `web/service/api_token.go` (فایل جدید): `ApiTokenService` با `CreateToken`/`List`/`Rotate`/`Delete`/`ResolveScope` — gate یا token اشتراکی (دسترسی کامل) یا یک token محدود (scope را چک می‌کند) را قبول می‌کند. `tokenGate` در `web/controller/node_api.go` بازمی‌نویسی شد (`routeScopeForPath` + `ResolveScope`، بدون `crypto/subtle` — مقایسه در api_token.go). ۴ مسیر جدید در `web/controller/node.go`: `GET /panel/api/nodes/scopedTokens`, `POST /panel/api/nodes/scopedTokens`, `POST .../rotate/:id`, `POST .../del/:id` (handlerهای `listScopedTokens`/`createScopedToken`/`rotateScopedToken`/`delScopedToken`). یک بخش «Scoped API tokens» روی `web/html/nodes.html` (جدول + فرم mint + rotate/delete) + i18n `pages.nodes.scopedTokens*`/`scopeNodeRead`/`scopeNodeWrite`/`scopeHeartbeat`/`tokenDeleted`/`rotated` (en + fa).

**Build**: `GOOS=linux go build ./...` + `go vet` سبز.


## ۰e. فاز ۵ — control-plane سبک NX-style

هفت ارتقای control-plane که پنل را از یک داشبورد به یک سطح مدیریت واقعی
ارتقا می‌دهد:

- **2FA TOTP (per-admin)**: هر ادمین یک secret TOTP روی ردیف خودش دارد
  (`web/service/user.go` → `SetTwoFactor`، `verifyTOTP`). log-in دو مرحله‌ای:
  پس از رمز، اگر `TwoFactorEnable` فعال باشد، کد TOTP لازم است. کلیدهای
  `twoFactorEnable`/`twoFactorToken` قدیمی panel-global حذف شدند تا secret
  مشترک به همه‌ی userها نشت نکند. testهای `admin_test.go` روی TOTP skew
  tolerance + round-trip + migration پوشش دارند.
- **Session management**: `model.UserSession` (جدول `user_sessions`) یک shadow
  log از نشست‌های فعال است: هر login یک fingerprint ۳۲-هگز می‌سازد و ردیف
  می‌سازد؛ admin می‌تواند «لغو همه‌ی نشست‌ها» بزند (blanket revoke) یا یک
  نشست خاص را revoke کند. `web/service/user_session.go` +
  `web/controller/admin.go` + `web/html/admins.html` (ستون «نشست‌های فعال»
  با دکمه‌ی لغو). prune هر ۶۰ ثانیه.
- **Webhooks outbox**: `model.WebhookDelivery` (جدول `webhook_deliveries`) هر
  POST webhook را log می‌کند (url, event, status, response code, timestamp).
  `web/service/webhook.go` → `recordDelivery` + `PruneWebhookDeliveries` (cap
  ۵۰۰ به‌ازای هر url) + `ListWebhookDeliveries`. مسیر
  `GET /panel/setting/webhook/deliveries` + prune. prune هر ۶۰ ثانیه.
- **Backup encryption**: `EncryptBackupAES`/`DecryptBackupAES` (scrypt ۶۰۰k
  + AES-256-GCM) در `web/service/server.go`. مسیر
  `GET /panel/getDbEncrypted?passphrase=` و `POST /panel/importDBEncrypted`
  (فایل .vpnui + passphrase). خروجی یک فایل self-contained با header نسخه‌دار
  `[0x01][32 salt][12 nonce][ciphertext+tag]`.
- **Ctrl-K command palette**: `web/html/component/aSidebar.html` — دکمه‌ی
  `⌘K`/`Ctrl-K` یک جست‌وجوگر از تمام tabs مجاز (permission-gated) + اکشن‌های
  سریع (refresh, logout). ArrowUp/Down/Enter/Esc. i18n `menu.paletteTitle` +
  `pages.palette.*` en_US + fa_IR.
- **PWA manifest + service worker**: `web/assets/manifest.json` (installable
  روی موبایل، theme color، icon) + `web/assets/sw.js` (cache-first برای assets،
  network-first برای صفحه، scope = base path). `<link rel="manifest">` +
  `<script>` SW registration در `page.html`.
- **Roles/limits (permissions)**: `model.Permission` bitmask +
  `model.AllPermissions` + `PermAccessInbounds`/`PermCreateInbound`/...
  موجود است؛ super admin همه را دارد، reseller mask مشتق‌شده. مسیر
  `GET /panel/admins/permissions` list canonical slugs را می‌دهد.

**Build**: `GOOS=linux go build ./...` + `go vet` سبز.


## ۰d. فاز ۴ — inbound clone + mTLS + tunnel health + CPU/mem history

چهار ارتقا که همگی additive هستند:

- **Inbound clone to nodes**: اپراتور هاب یک اینباند محلی و یک نود مقصد را
  انتخاب می‌کند؛ هاب از پنل نود (از طریق API token-gated) می‌خواهد یک
  اینباند هم‌پیکر بسازد (همان پروتکل + stream settings، پورت آزاد نود،
  اعتبارنامه‌های تازه‌ساخته‌ی نود) و سپس تمام حساب‌های فعال اینباند مبدأ را
  روی اینباند جدید نود provision می‌کند، هر کدام را به‌عنوان NodeAccount ثبت
  می‌کند. حذف نود بعدی cascade می‌شود و حساب‌ها را purge می‌کند.
- **mTLS bundle**: کلید و گواهی mTLS پنل (mtls.go) حالا روی صفحه‌ی Nodes
  به‌عنوان CA cert قابل‌استفاده است تا اپراتور بتواند آن را روی فیلد
  `ApiCaPem` یک نود (verify mode) pin کند.
- **Tunnel health monitor**: واچ‌داگ حالا روی هر transition وضعیت (stall،
  recover به up، نتیجه‌ی link-test) یک `TunnelHealthEvent` ثبت می‌کند؛ جدول
  `tunnel_health_events` به ۲۰۰ ردیف آخر به‌ازای هر تونل prune می‌شود.
  روی صفحه‌ی Tunnels یک اکشن «Health» با timeline اضافه شده.
- **CPU/RAM/Net history**: نمونه‌های گِیج CPU/RAM/Net نودها روی جدول
  `node_metrics` ذخیره می‌شوند (هر heartbeat/poll/loca beat) و به ۲۴ ساعت
  prune می‌شوند. روی مودال manage نود، یک کارت «CPU / RAM / Net history»
  با progress bar و جدول نمونه‌های اخیر (نرخ delta-computed) اضافه شده.

**Build**: `GOOS=linux go build ./...` + `go vet` سبز.


## ۰b. فاز ۲b — ری‌لای ترافیک واقعی کاربر روی تونل [کامیت 0578c83]

موتور تونل در فاز ۲ کانال control را دستیابی و keepalive می‌کرد، ولی
forwarded-port mapها در عمل بی‌اثر بودند: listener محلی وجود نداشت و بایتی
کاربر روی سیم نمی‌رفت. فاز ۲b مسیر واقعی ترافیک کاربر را ساخت:

- **`web/service/tunnel/relay.go`** (جدید):
  - `streamReg` — وضعیت stream‌های یک کانال control: جدول remote (stream‌هایی
    که همتا باز کرده و این انتها backendشان را dial کرده) + جدول local
    (اتصال listener محلی که زیر source port خودش park شده)
  - `writeFrame / readFrame` — فریم مولتیپلکس `[type][stream][len][payload]`
  - `pumpFrames` — ریپر کانال control: فریم‌های open/data/close را به طرف
    محلی fan-out می‌کند؛ روی open، backendی که source port مشخص کرده را dial
    می‌کند و دوطرفه pipe می‌کند
  - `startLocalListeners` — باind پورت‌های map روی 0.0.0.0 (TCP + UDP)
  - `relayLocalTCP / relayLocalUDP` — نصف ارسال: بایت‌های اتصال محلی را به‌عنوان
    فریم data با id = source port به تونل می‌فرستد
  - `resolveBackend / sortPorts` — lookupهای map که relay برایشان key می‌گیرد
- **`manager.go`**: `handle` گیج‌های `bytesUp/bytesDown` گرفت (ترافیک کاربر،
  keepalive را نمی‌شمارد)؛ `runRole` حالا `streamReg` می‌سازد و روی کانال
  control، هم `startLocalListeners` و هم `pumpFrames` را راه می‌اندازد.
  Accessorهای `BytesUp/BytesDown` برای داشبورد.
- **`core.go`**: stubهای قدیمی `pumpStream/relayBackend/pumpUDP` حذف شد؛
  `pumpFrames` تنها ریدر کانال control است.

توپولو (موقعیت معکوس ایران ⇄ خارج): هر انتها listener محلی‌اش را روی پورت‌های
map‌اش بایند می‌کند؛ اتصال پذیرفته‌شده یک stream روی کانال control می‌شود که
idاش source port است؛ همتا آن port را با map خودش resolve و backend را dial
می‌کند. مکانیزم در جهت معکوس با تعویض نقش‌ها همین است — یک تونل دوطرفه.

**Build**: `GOOS=linux go build ./...` + `go vet` سبز.


## ۰c. فاز ۳ — ارتقای صفحه‌ی اشتراک به سبک 3X-UI

صفحه‌ی اشتراک (subpage) به سبک 3X-UI ارتقا یافت:

- **App install grid**: به‌جای دو dropdown (Android/iOS)، یک گرید کارت
  (V2Box, V2RayNG, Sing-box, V2RayTun, Happ, Incy, Shadowrocket, Streisand)
  که با تشخیص user-agent پلتفرم، کارت‌های مناسب را نشان می‌دهد. هر کارت یک
  deep link واقعی به صفحه‌ی import/install آن اپ است — بدون کپی-پیست.
- **صفحه‌ی سفارشی اشتراک**: اپراتور می‌تواند HTML دلخواهش را در تنظیمات
  (Settings → Panel → Subscription → Custom Subscriber Page) بچسباند. پلوس‌هولدرها:
  `{USER} {REMAINING} {TOTAL} {USED} {UPLOAD} {DOWNLOAD} {EXPIRE} {SUB_URL}`
  `{SUB_JSON} {SUB_CLASH} {BASE_URL} {LINKS}`. اگر HTML خالی باشد، صفحه‌ی
  پیش‌فرض پنل render می‌شود. کد HTML اپراتور از html/template نمی‌گذرد
  (اجرای `c.String(200, ...)` مستقیم) تا `{{...}}` داخلش 500 ندهد.
- **i18n**: کلیدهای `subPageTemplate` / `subPageTemplateDesc` در en_US + fa_IR.
- **Build**: `GOOS=linux go build ./...` + `go vet` سبز.


## ۰. فاز ۱ — نودِ قوی (heartbeat + آپدیت از هاب) [کامیت 965ead2]

مطابق نقشه‌راه در `PANEL_COMPETITION_ANALYSIS.md` (گزارش مقایسه‌ای با ۳ پنل رقیب:
3X-UI، BackPack و NX Panel)؛ نخستین فاز اجرا شده:

- **Heartbeat push-based نود**: پنل «نود» هر ۳۰ ثانیه گِیج‌های سیستمی خودش
  (CPU، RAM، uptime، counterهای شبکه، state/core xray) را به پنل «هاب» POST می‌کند
  (`POST /panel/node/heartbeat`). هاب این را روی ردیف نود ثبت می‌کند؛ polling ۶۰ثانیه‌ای
  فعلی هم همچنان می‌ماند (legacy + offline-detect).
- **Node model**: فیلدهای `LastHeartbeat / CpuPct / MemPct / UptimeSecs / NetUp /
  NetDown / XrayState / XrayError / LastError` (افزودنی، صفر = بدون داده).
- **NodeStatus**: همان فیلدها روی wire format؛ حالت poll و push یک شکل دارند.
- **صفحه‌ی Nodes**: ستون‌های جدید «CPU/RAM · ترافیک» و «وضعیت Xray» (legacy = "—")
  + دکمه‌ی **«Update panels»** (bulk `POST /panel/api/nodes/updatePanels`).
- **آپدیت پنل از هاب**: `POST /panel/node/updatePanel` (token-gated روی نود) که آپدیت
  خودکار همان پنل را در background اجرا می‌کند؛ هاب نتیجه‌ی «۳ از ۵ شروع شد» می‌بیند.
- **Stale-heartbeat offline-detect**: نودی که ۳ هارت‌بیت پیاپی (۹۰ثانیه) را گم کرده
  آفلاین محسوب می‌شود حتی اگر poll endpoint هنوز جواب بدهد.
- **i18n**: کلیدهای جدید در `en_US` و `fa_IR`.
- **Build**: `GOOS=linux go build ./...` سبز.

### فازهای پیشِ‌رو (در نقشه‌راه)
- فاز ۲: ادغام کامل کد **BackPack** (تونل زنی ۱۲ ترنسپورت + L3 + link-test +
  failover + watchdog)
- فاز ۳: صفحه‌ی **3X-UI subpage** (app icons، balancers، template سفارشی، external import)
- فاز ۴: inbound clone + mTLS + tunnel health monitor + CPU/mem history
- فاز ۵: control-plane سبک NX (roles/limits، 2FA TOTP، session mgmt، webhooks outbox،
  backup encryption، Ctrl-K palette، PWA)
- فاز ۶: fallbacks + MTProto quota + calendar renewal + API scoped tokens


## ۱. هدف کلی

ساختن «بهترین پنل روی گیت‌هاب» روی فوِیک ۳x-ui که:
- از Nexora/Remnawave/Marzban جلوتر باشد: تنوع پروتکل، تونل، مولتی‌نود، مانیتورینگ زنده، اتوماسیون، UI تمیز و خسته‌کننده‌نشدن.
- قابلیت **Node Federation**: کاربر فقط **Name + Token** نود را وارد کند؛ Base URL، API و اتصال بین سرورها خودکار تنظیم شود و تونل پرسرعتی بین سرور مبدأ (مثلاً ایران) و مقصد (مثلاً هلند) ایجاد شود.
- وقتی نود حذف می‌شود، اکانت‌های پروویژن‌شده روی سرور مقصد **خودکار پاک** شوند.
- پریست‌های اینباند واقعاً اینباند کارفرما بسازند (تنظیمات بهینه، ساب‌دامن/CDN، پورت رندوم آزاد).
- گروه‌بندی کلاینت‌ها: انتخاب چند کاربر + افزودن همه به گروه با یک کلیک.
- نصب یک‌خطی: `curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash`

---

## ۲. قابلیت‌هایی که تکمیل شد (با جزئیات)

### ۲.۱ حذف زنجیره‌ای نود (Node Delete Propagation)
- **مسئله**: وقتی نود از سرور مبدأ (hub) حذف می‌شد، اکانت‌هایی که روی سرور مقصد پروویژن شده بود **هنوز زنده** می‌ماندند.
- **راه‌حل**:
  - اندپوینت جدید سمت نود: `POST /panel/node/purge` (`purgeByHub` / `purgeEmails` / `inboundHasAnyEmail` در `web/controller/node_api.go`)
  - هاب قبل از حذف، ایمیل‌های `NodeAccount` آن نود را جمع می‌کند و `RemotePurgeByHub` را صدا می‌زند (best-effort: اگر نود آفلاین باشد، حذف محلی بلاک نمی‌شود)
  - `NodeService.Delete` ردیف‌های محلی `NodeAccount` را هم cascade می‌کند
- **ایمنی**: purge کلیدش هویت هاب (`X-Hub-Name` + لیست ایمیل‌هاست)؛ اگر نود بین چند هاب مشترک باشد فقط اکانت‌های همان هاب پاک می‌شود.

### ۲.۲ ارتقای اینباند فدریشن به VLESS + Reality (بهترین نود)
- **مسئله**: اینباند فدریشن قبلاً **TCP ساده بدون امنیتی** بود — ترافیک کلاینت↔نود تشدید‌نشد و قابل‌شناسایی DPI بود.
- **راه‌حل** (`web/service/inbound.go` → `CreateNodeFedInbound`):
  - اینباند VLESS فدریشن حالا **VLESS + Reality** ساخته می‌شود: x25519 تازه‌تولید، چند SNI (microsoft/google/cloudflare/bing/apple)، fingerprint chrome، `shortIds`
  - هر اکانت VLESS روی این اینباند **auto-XTLS Vision** می‌گیرد (`NodeAddClient` وقتی flow خالی است)
  - پورت‌ها با **اسکن DB + bind kernel** انتخاب می‌شوند (دو اینباند پشت‌سرهم دیگر روی پورت یکدیگر نمی‌نشینند)
  - VMess می‌ماند TCP ساده (fallback قابل‌دایال)
- **نتیجه**: لینک اشتراک‌سازی `vless://...?security=reality&pbk=...&flow=xtls-rprx-vision` می‌شود — تشدید‌شده و ضد-سنت‌بلاک؛ دقیقااً «تونل نود» که ترافیک ایران↔هلند می‌رود.

### ۲.۳ مودال «اتصال + سرعت» نود (حذف API/Provision)
- **مسئله**: بخش‌های API / hot-add / hot-remove / provision از مودال Manage حذف می‌شد؛ فقط وضعیت اتصال + سرعت لازم بود.
- **راه‌حل** (`web/html/nodes.html`):
  - مودال جدید **Connection** نمایش می‌دهد: وضعیت آنلاین/آفلاین + آخرین بازدید + **IP خروجی نود** + **latency** + **سرعت زنده up/down** + لیست read-only اینبایندهای نود
  - کلیدهای i18n جدید: `connection`, `connStatus`, `speed`, `exitIp`, `latency` (فارسی + انگلیسی)

### ۲.۴ مولتی‌سلیکت گروه کلاینت‌ها
- **مسئله**: دکمه «Add clients to group» فقط یک کاربر را اضافه می‌کرد.
- **راه‌حل** (`web/html/clients.html`):
  - `a-select` گروه **multi-select** شد (`_addIds`)
  - با یک کلیک روی «Add to group»، **همه کاربرهای انتخاب‌شده** در یک commit یکجا به گروه اضافه می‌شوند (projection اینباند فقط یک‌بار اجرا می‌شود)

### ۲.۵ رفع «اتچ نشدن» کانفیگ نود (No inbounds)
- **مسئله**: وقتی `BaseUrl` نود خالی بود، `NodeAccountLink` لینک نمی‌ساخت و اشتراک «No inbounds» نشان می‌داد.
- **راه‌حل** (`sub/subService.go` → `NodeAccountLink`):
  - وقتی `BaseUrl` خالی است، به **listen خود snapshot** برمی‌گردد — مگر listen `0.0.0.0` باشد که نمی‌شود
  - پلر خودکشفی BaseUrl در چرخه بعدی آن را ارتقا می‌دهد

### ۲.۶ رفع خطای ۴۰۴ API هنگام استفاده از base_path (v3.0.0)
- **مسئله**: وقتی پنل زیر base_path غیرخالی (مثلاً `/subs/`) اجرا می‌شد، ۷ تا toast "Request failed with status code 404" روی Overview/Inbounds/Clients/Nodes نمایش داده می‌شد.
- **ریشه‌یابی**: `common/page.html` مقدار `basePath` را از `{{ .base_path }}` می‌گرفت و آن را به `axios.defaults.baseURL` می‌داد. اما صدها فراخوانی API در کل پنل با مسیر مطلق `/panel/api/...` نوشته شده بودند. axios وقتی URL مطلق باشد (با `/` شروع می‌شود)، `baseURL` را نادیده می‌گیرد. در نتیجه همه درخواست‌ها به ریشه‌ی سرور می‌خوردند به‌جای `/subs/panel/api/...` و سرور ۴۰۴ برمی‌گرداند.
- **رفع** (`web/html/common/page.html`): یک axios request interceptor اضافه شد که هر URL با پیشوند `/panel/` را به مسیر نسبی تبدیل می‌کند تا axios بتواند `baseURL` را به‌درستی در پیشوند آن بگذارد. همین‌طور `HttpUtil` (در `web/assets/js/util/index.js`) حالا پاسخ‌های ۴۰۴ را به‌صورت بی‌صدا (log-only) برمی‌گرداند تا در صورت ناهماهنگی نسخه‌ی HTML با binary، پیام‌های تکراری نمایش داده نشود.

---

## ۳. باگ‌های CI که deploy را خراب کرده بود (همه رفع شد)

| # | باگ | ریشه | رفع |
|---|-----|------|-----|
| 1 | **Test step** شکست | `TestDonateAddressesMatchReadme` — README.md به یک 줄 تقلص کرده بود و بخش «## Donate» را نداشت | بازسازی README با ۷ آدرس donate که با `donateAddresses` در کد مطابق دارد |
| 2 | **Logger permission** | تست‌ها `logger.InitLogger` را بدون root صدا می‌زدند و `/var/log/vpn-ui` قابل‌ساخت نبود | `initTestLogger(t)` در `web/controller` + `VPNUI_LOG_FOLDER` به temp dir + `ensureLogger()` nil-safe در `logger/logger.go` |
| 3 | **Build step** — `unexpected EOF while looking for matching '"'` | خط `STAMP_REPO="${VPNUI_REPO:-$(... sed -E '...')}"` در `build.sh` — single-quote داخل `$(...)` داخل `"..."` parse bash را خراب می‌کرد | بازنویسی با `${var#prefix}` / `${var%.git}` خالص بدون quote تودرتو |
| 4 | **404 toasts** با base_path | فراخوانی‌های API با مسیر مطلق `/panel/api/...` نوشته شده بودند و axios `baseURL` را نادیده می‌گرفت | افزودن interceptor برای rewrite مسیرهای `/panel/` + بی‌صدا کردن ۴۰۴ در HttpUtil |

**نتیجه**: CI run #11 (v2.9.8) و run #42 (v3.0.0) سبز شدند — test + build + release publish.

---

## ۴. وضعیت دپلوی

- **کد**: همه‌چیز روی `unique/main` (آخرین commit: `f9f170f`)
- **تگ**: `v3.0.0`
- **Release**: `https://github.com/Miladizadpanah/UniQue/releases/tag/v3.0.0` با binary `vpn-ui-amd64`
- **نصب یک‌خطی**:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash
  ```

---

## ۵. رفع پنالِ خالی (Blank Panel)

### علامت
- صفحات `inbounds`، `clients` و `nodes` **پس‌زمینه سفید کاملااً خالی** رندر می‌شدند.

### ریشه
- در `web/controller/util.go`، مقادیر `perms` و `reseller` (mapها) بدون marshal کردن JSON مستقیماً در `<script>` قرار می‌گرفتند که در JS نامعتبر است.
- همچنین `body_scripts` در `page.html` بعد از body content load می‌شد و Vue/axios دیر load می‌شدند.

### رفع
- `web/controller/util.go`: اضافه کردن `encoding/json` + marshal کردن `perms` و `reseller` mapها به JSON (`permsJSON`، `resellerJSON`)
- `web/html/common/page.html`: انتقال `body_scripts` به `page/head_end` + استفاده از `.permsJSON` و `.resellerJSON`

---

## ۶. فایل‌های کلیدی (مراجعه سریع)

| حوزه | مسیر |
|------|------|
| حذف زنجیره‌ای نود (hub) | `web/controller/node.go` → `del` |
| اندپوینت purge (نود) | `web/controller/node_api.go` → `purgeByHub` |
| سرویس purge | `web/service/node.go` → `RemotePurgeByHub`, `NodeService.Delete` |
| اینباند Reality فدریشن | `web/service/inbound.go` → `CreateNodeFedInbound`, `fedRealityStream` |
| flow VLESS auto | `web/service/inbound.go` → `NodeAddClient` |
| لینک نود در اشتراک | `sub/subService.go` → `NodeAccountLink`, `nodeHostFromBaseUrl` |
| مودال اتصال نود | `web/html/nodes.html` → `manageModal`, `nodeHost`, `inbCols` |
| مولتی‌سلیکت گروه | `web/html/clients.html` → `g._addIds`, `addToGroup` |
| رفع ۴۰۴ با base_path | `web/html/common/page.html` → interceptor + `web/assets/js/util/index.js` → 404 silent |
| logger nil-safe | `logger/logger.go` → `ensureLogger` |
| تست logger | `web/controller/log_helper_test.go` → `initTestLogger` |
| بازنویسی build.sh | `build.sh` → بلوک `STAMP_REPO` |
| README donate | `README.md` → بخش `## Donate` |
| install.sh / deploy.sh | `install.sh`, `deploy.sh` → REPO default = `Miladizadpanah/UniQue` |

---

## ۷. چک‌لیست تست زنده (بعد از آپدیت به v3.0.0)

1. روی سرور `sudo bash install.sh` بزنید یا `curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash`
2. مرورگر را **hard refresh** کنید (Ctrl+Shift+R) تا assets قدیمی پاک شوند
3. Overview → باید بدون toast قرمز ۴۰۴ بالا بیاید
4. Nodes → یه نود جدید با **Name + Token** اضافه کن → pairing approve → `vpnui-fed-vless` با `security: reality`
5. Clients → چند کاربر بساز + به نود attach → لینک اشتراک `security=reality&pbk=...&flow=xtls-rprx-vision` با **exit IP نود**
6. گروه‌بندی → چند کاربر انتخاب + **Add to group** → همه در یک commit
7. Connection modal نود → سرعت زنده + IP خروجی + latency
8. حذف نود → اکانت‌های روی مقصد **خودکار purge** بشن
9. اگه پنل زیر base_path غیرخالی اجرا می‌شه (مثلاً `/subs/`): **همه pages باید بدون ۴۰۴ کار کنن**

---

## ۸. خلاصه نسخه‌ها

| نسخه | خلاصه |
|------|-------|
| v2.9.2 | پریست‌های کارفرما، خودکشفی نود، mTLS build-stamp |
| v2.9.3 | حذف زنجیره‌ای نود + مودال اتصال + گروه مولتی‌سلیکت |
| v2.9.4 | ارتقای اینباند فدریشن به VLESS+Reality |
| v2.9.5 | رفع باگ CI test (logger + README) |
| v2.9.6 | رفع باگ CI build (باگ واقعی donate README) |
| v2.9.7 | رفع نهایی CI (بازنویسی STAMP_REPO) + release سبز + binary منتشرشده |
| v2.9.8 | نصب/آپدیت‌ر به سمت UniQue + رفع i18n در clients/nodes |
| **v3.0.0** | **رفع ۴۰۴ با base_path + بی‌صدا کردن ۴۰۴ در HttpUtil + release روی UniQue** |

---

## ۹. وضعیت نهایی

- ✅ ۵ قابلیت اصلی تکمیل و کامیت‌شده
- ✅ ۴ باگ رفع‌شده (CI + 404)
- ✅ release v3.0.0 با binary منتشرشده روی `Miladizadpanah/UniQue`
- ✅ CI سبز (test + build + publish)
- ✅ **بلانک پنل کاملاً رفع‌شده**
- ✅ **تم مدرن و خوشگل اضافه‌شده**
- ✅ **خطاهای ۴۰۴ API هنگام استفاده از base_path رفع‌شده**
