# تحلیل رقابتی پنل‌ها + نقشه‌راه «بهترین پنل»

> تاریخ: ۱ نوامبر ۲۰۲۶ · پنل مرجع: **VPN-UI (UniQue) v3.0.0** (این ریپازیتوری)
> رقبا: **3X-UI v3** · **BackPack (تونلینگ)** · **NX Panel (control plane)**

---

## ۱. خلاصه‌ی اجرایی

پنل ما (UniQue) پایه‌ی بسیار قوی‌تری از ۳X-UI دارد:

| توان | UniQue (ما) | 3X-UI v3 |
|---|---|---|
| پروتکل‌های VPN کلاسیک | ✅ WireGuard, AmneziaWG (userspace, بدون DKMS), OpenVPN, OpenConnect, PPTP, L2TP, IKEv2, GRE, SSH, SSTP | ✅ فقط داخل xray: WireGuard/AWG/TUIC |
| پروتکل‌های xray | ✅ VLESS, VMess, Trojan, SS2022, Hysteria2, TUIC, Naive, MTProto, XHTTP | ✅ همان‌ها + AmneziaWG + native TUIC server + MTProto (بهترین اجرا) |
| Outbound WARP/Nord/PIA | ✅ | ✅ (خوب‌تر: masque/egress trace) |
| نود فدریشن | ✅ (token-based pairing) | ✅✅ (heartbeat-push, in-flight updates, tree, mTLS, token encryption) |
| سابکریپشن | ✅ (صفحه‌ی مدرن + JSON + Clash) | ✅✅ (صفحه‌ی بهتر + app icons + subscription balancers + external + hwid) |
| تونل زنی متعدد | ❌ فقط inbound-to-inbound | ❌ |
| — BackPack | ✅ ۱۲ ترنسپورت + L3 + failover + link-test | — |
| مانیتورینگ | ✅ Prometheus + Grafana + webhook | ✅ + **tunnel health monitor** (probe-through-proxy + auto-restart) |
| TG/Discord bot | ✅ tgbot | ✅✅ (wizard افزودن کلاینت، reports، levels، broadcast) + Discord |
| نقشه‌ها | ✅ Reseller | ✅ Multi-admin + roles |

**نتیجه:** ما از نظر تنوع پروتکل جلو هستیم؛ عقب‌ماندگی ما در **۳ محدوده** است:
1. **تونل‌زنی** (BackPack به‌طور کامل)
2. **سیستم نود** (push-based heartbeat 3X-UI)
3. **صفحه‌ی سابکریپشن** (دقت و تمیزکاری 3X-UI)

---

## ۲. ماتریس کامل قابلیت‌ها

### ۲.۱ BackPack → تونل‌زنی

| قابلیت | توضیح | وضعیت در پنل ما | اولویت |
|---|---|---|---|
| **Reverse tunnel** | کلاینت (kharej) به ایران dial می‌کند؛ پورت‌های فورواردشده | ❌ | ⭐⭐⭐ |
| **Direct L3 tunnel (GRE)** | tun-to-tun با encapsulation + Noise + MTU auto + carriers | ❌ | ⭐⭐⭐ |
| **۱۲ ترنسپورت** | TCP, TCP Mux, TCP+Stealth(Noise), TCP+PCK, UDP, UDP+KCP+FEC, UDP+QUIC, WS, WS Mux, WSS, WSS Mux, xDi(ICMP) | ❌ | ⭐⭐⭐ |
| **Link Test** | اندازه‌گیری latency/jitter/loss روی مسیر + پیشنهاد ترنسپورت | ❌ | ⭐⭐ |
| **Failover آدرس‌ها** | چند آدرس سرور + failover خودکار + health-scoring | ❌ | ⭐⭐ |
| **Transport fallback chain** | اگر ترنسپورت فعال افتاد، به زنجیره‌ی بعدی برو | ❌ | ⭐⭐ |
| **PROXY protocol v2** | IP واقعی کلاینت پشت تونل | ❌ | ⭐ |
| **Forwarded UDP** | فوروارد UDP کنار TCP | ❌ (ما policy routing UDP را داریم؛ BackPack per-tunnel toggle دارد) | ⭐ |
| **Performance presets** | Balance/Turbo/Aggressive/Throughput + kernel tuning | ❌ | ⭐ |
| **Watchdog self-healing** | تونل گیرکرده → recovery خودکار | ❌ (monitoring webhook داریم) | ⭐⭐ |
| **Managed servers** | سرورهای remote را در پنل ثبت کن، از panel دو سر تونل بساز | ❌ (pairing ما نزدیک است) | ⭐⭐ |
| **Telegram alerts** | report دوره‌ای + آлерت منابع + recovery | ✅ (tgbot داریم) | — |
| **Backup/Restore portable** | tar.gz شامل config + TLS + telegram + scheduled | جزئی | ⭐ |
| **IP spoofing** | SNAT/SPoof برای آدرس‌های DIRTY/فیلترشده | ❌ | ⭐ |
| **MSS clamp** | auto-tune MSS روی مسیرهای L3 | ❌ | ⭐ |

### ۲.۲ 3X-UI → سیستم نود قوی‌تر

| قابلیت | توضیح | وضعیت ما | اولویت |
|---|---|---|---|
| **Heartbeat push-based** | نود هر ۳۰ ثانیه به hub گزارش می‌دهد؛ hub فقط poller برای offline-detect است | ❌ (ما pull-only: hub poll می‌کند) | ⭐⭐⭐ |
| **Node CPU/RAM/Uptime metrics** | در heartbeat: cpuPct, memPct, uptimeSecs, netUp/netDown | ❌ (فقط latency + counts) | ⭐⭐ |
| **Xray state on node** | xrayState + xrayError + configDirty + lastError در هر نود | ❌ | ⭐⭐ |
| **Inbound clone to nodes** | کلید clone: اینباند هاب → روی نود ساخته/به‌روز می‌شود | ❌ (فederation inbound ثابت داریم) | ⭐⭐ |
| **Inbound sync mode** | all / by-tag انتخابی (InboundTags) | ❌ | ⭐ |
| **Node tree (transitive)** | hub → node → node-of-node (ParentGuid) | ❌ | ⭐ |
| **Update panels from hub** | hub روی نودها `update.sh` اجرا می‌کند (stable/dev) | ❌ | ⭐⭐ |
| **mTLS between panel↔node** | CA bundle + pinned cert SHA256 + trust CA list | جزئی (token فقط) | ⭐⭐ |
| **Node token encryption at rest** | AES keyring file, modes off/migration/required | ❌ (token plaintext) | ⭐ |
| **Node health job** | offline threshold، descendant heartbeats | ❌ | ⭐ |
| **Sponsor / API docs page** | RESTful API با token scoped + reference | ❌ (api.go داریم؛ scoped token نه) | ⭐ |
| **PWA install** | manifest + install prompt | ❌ (README ما ادعای 3x-ui است) | ⭐ |

### ۲.۳ 3X-UI → سابکریپشن (صفحه‌ی کاربر)

| قابلیت | توضیح | وضعیت ما | اولویت |
|---|---|---|---|
| **App icons grid** | لوگوی ۱۰ اپ (Happ, Incy, Shadowrocket, Sing-box, Streisand, V2Box, V2RayNG, V2RayTun) + platform detect + open/copy | ✅ (ما dropdown ساده داریم؛ 3x-ui کارت‌های لوگو + deep-link درست‌تر) | ⭐⭐ |
| **Copy all / per-link copy** | ✅ | ✅ | — |
| **QR per-format** (sub/JSON/Clash) | ✅ | ✅ | — |
| **Custom sub page templates** | operator template HTML با placeholders (`{USER}`, `{ROUTE}`, `{REMAINING}`) | جزئی (remark model) | ⭐ |
| **Subscription balancers** | چند اینباند = یک balancer؛ خروجی sub با پروتکل balancer | ❌ | ⭐⭐ |
| **External subscription import** | لینک sub بیرونی به‌عنوان inbound + quota از خود sub | ❌ | ⭐ |
| **Happ crypto links** | happ:// با key exchange | ❌ | ⭐ |
| **Incy/Salamander URI** | سازگاری URI جدید | ❌ | ⭐ |
| **HWID device limits in sub** | hardware id از client + enforce | ✅ (device-limit-plan) | — |
| **Sub page: language toggle, RTL, announce, support link, update interval footer** | ✅ | ✅ (ما hero+ring+stats+meta+links+QR+configs+apps) | — |
| **Info endpoint for apps** (sub-info headers) | `total`, `expired`, `upload`... در header پاسخ | ✅ | — |

### ۲.۴ 3X-UI → پروتکل/امکاناتی که ما نداریم

| قابلیت | توضیح | اولویت |
|---|---|---|
| **Fallbacks** | چند پروتکل روی یک پورت (VLESS+Trojan on 443) | ⭐⭐ |
| **MTProto per-client quotas + ad-tags + live apply** | ما MTProto داریم (mtproto.go)؛ 3x-ui quota + fake-tls secrets بهتری دارد | ⭐ |
| **Xray reverse proxy mode** (xray_reverse_modal.html) | ما داریم | — |
| **Client keepalive range / scheduled renewal (calendar)** | چرخه‌ی تمدید خودکار هفتگی/ماهانه با preview | ⭐⭐ |
| **Fail2ban IP-limit enforcement** | ما nftables داریم (nftables.go, ip-limiter-plan) — قوی‌تر | — |
| **WARP IP change job** (warp_ip_job) | ما warp.go داریم | — |
| **AmneziaWG kernel vs userspace toggle** | ما userspace (awgsrc) — بهتر | — |

### ۲.۵ NX Panel → control plane (idéal)

| قابلیت | توضیح | اولویت |
|---|---|---|
| **Multi-operator roles (main admin/operator/reseller/custom)** | ما reseller داریم؛ roles سفارشی + limits (max users, max device limit, max plan length) | ⭐⭐ |
| **Session management** | list/revoke open sessions + TOTP 2FA + recovery codes | جزئی (two_factor_modal.html داریم؛ session list نه) | ⭐⭐ |
| **Login rate-limit + ban list + allowlist + trusted_proxies** | ما security.html داریم؛ ban-list UI را چک کن | ⭐ |
| **Webhooks outbox** (signed events, retries, replay) | ما webhook.go داریم؛ outbox + signing | ⭐ |
| **Backup: encrypted (scrypt+AES-GCM), scheduled, pre-restore snapshot, cross-DB** | ما backup داریم؛ encryption + schedule | ⭐⭐ |
| **Self-update with checksum + auto-rollback** | ما panelupdate.go داریم | ⭐ |
| **Ctrl-K command palette** | جست‌وجو در صفحات/فرم‌ها/کاربران | ⭐ |
| **Domain fronts / CDN wildcard (`*.cdn`) per-inbound** | ما cloudflare.go داریم؛ wildcard front | ⭐ |
| **IPv6 wildcard bind** | بررسی کنیم | ⭐ |
| **License system** | خارج از دامنه | — |
| **Recovery CLI (admin reset-password offline)** | ما vpn-ui.sh CLI داریم | ⭐ |

### ۲.۶ مانیتورینگ (3X-UI)

| قابلیت | اولویت |
|---|---|
| **Tunnel health monitor** (probe URL through proxy, N failures → restart xray, cooldown) — env-driven | ⭐⭐⭐ (ما دقیقاً همین را برای تونل‌های BackPack-style نیاز داریم) |
| **CPU/mem history job** | ⭐⭐ |
| **Stats notify job (TG/Discord on threshold)** | ⭐ |

---

## ۳. معماری پیشنهادی ادغام BackPack

### ۳.۱ تصمیم لایسنس (مهم)
- BackPack: **AGPL-3.0** → «based on BackPack» در README/NOTICE الزامی است؛ نام/لوگوی BackPack مجاز نیست.
- پنل ما: GPL-v3 (3x-ui fork) → AGPL در go.mod dependency **سازگار** است فقط اگر کد AGPL به‌عنوان **dependency** وارد شود و ما تغییرات AGPL فایل‌ها را منتشر کنیم.
- **راه‌حل پیشنهادی: `third_party/backpack/`** با submodule یا vendored source + لیسنسر-نوتیس در README (مثل `third_party/Xray-core`).
- ماژول‌های لازم: `internal/tunnel` (engine) + `internal/client/transport` + `internal/node` (managed servers) + `internal/telegram` (alerts) + `internal/sysstat` (metrics) + `internal/quota` (limits).
- **غیرضروری**: CLI/TUI, web panel جدا (ما خود ما پنل را داریم), localproxy, control wire (ما hub/node داریم).

### ۳.۲ نقشه‌ی اجزای پنل ما

```
vpn-ui
├── web/html/tunnels.html          ← UI جدید: لیست تونل‌ها + فرم wizard
├── web/controller/tunnel.go       ← REST: /panel/api/tunnels/*
├── web/service/tunnel/            ← service layer
│   ├── engine.go                  ← راه‌اندازی BackpackTunnel (client/server role)
│   ├── linktest.go                ← latency/jitter/loss + ترنسپورت پیشنهادی
│   ├── failover.go                ← multi-address + health scoring
│   └── watchdog.go               ← self-healing job
├── third_party/backpack/          ← vendored AGPL source
├── database/model: Tunnel, TunnelAddress, TunnelTransport...
└── monitoring: prometheus + grafana (تیک‌ها: backpack_tunnel_up, ..._bytes, ..._conn)
```

### ۳.۳ مدل داده
```go
type Tunnel struct {
  Id, Name, Role          // 'server'(iran) | 'client'(kharej)
  Direction               // 'reverse' | 'direct'
  Transport               // 'tcp' | 'tcpmux' | 'stealth' | 'pck' | 'udp' | 'kcp' | 'quic' | 'ws' | 'ws-mux' | 'wss' | 'wss-mux' | 'xdi'
  FallbackTransports      // JSON: ["quic","kcp","tcpmux"]
  Addresses               // JSON: [{host, port, priority, weight}]
  PortMappings            // JSON: [{local:"443", backend:"127.0.0.1:443"}, {local:"53/udp"...}]
  CarryUDP                bool
  Token                   // 64-char
  Tuning                  // 'balance' | 'turbo' | 'aggressive' | 'throughput'
  ProxyProtoV2            bool
  Enable                  bool
  Status                  // 'up' | 'down' | 'stalled'
  ActiveTransport         string
  LastError
  LatencyMs, JitterMs, PktLossPct
}
```

### ۳.۴ اتصال با موجودی پنل
- **Node pairing**: تونل = «نُودِ تونل‌دار»؛ هر تونل یک Node با `Type='backpack'` است. hub با heartbeat نود، status تونل را می‌بیند.
- **Inbound-to-tunnel**: بعد از ساخت تونل، کاربر می‌تواند در hub یک VLESS+Reality inbound بسازد و «carried via tunnel X» را انتخاب کند (carrier-tun pattern پنل ما: policy routing) — دقیقاً مدل `tunnel-carrier-plan.md`!
- **Sub link**: خروجی sub از نودِ تونل‌دار `exit ip` تونل می‌شود.

---

## ۴. نقشه‌راه اجرا (ترتیب پیشنهادی)

### فاز ۱ — «تنها-مهم» (تأثیر بلافاصله)
**۱.۱ Heartbeat-based node sync** (پوشش ۷۰٪ عقب‌ماندگی ما نسبت به 3X-UI)
- `database/model/Node.go`: افزودن `Guid`, `LastHeartbeat`, `CpuPct`, `MemPct`, `UptimeSecs`, `NetUp`, `NetDown`, `XrayState`, `XrayError`, `ConfigDirty`, `ActiveCount`, `DepletedCount`
- `web/controller/node_api.go` (سمت نود): `POST /panel/node/heartbeat` + token → metrics را push کند (هر ۳۰ ثانیه)
- `web/controller/node.go` (سمت هاب): polling فقط برای **offline-detect** (heartbeat > ۹۰ ثانیه = offline)
- job `node_health.go`: آستانه‌ی offline + descendant
- UI `nodes.html`: ستون‌های CPU/Mem/Uptime/Xray state + badge
- **تخمین**: ۳-۵ روز

**۱.۲ Node panel update from hub**
- hub: `POST /panel/api/nodes/updatePanels` → noderun `update.sh` (dev/stable) روی noderun
- UI: دکمه‌ی Update در نود + bulk-select (مثل 3X-UI NodesPage)
- **تخمین**: ۱-۲ روز

**۱.۳ Node token encryption at rest**
- keyring JSON (`XUI_NODE_TOKEN_KEY_FILE` style) + modes off/migration/required
- **تخمین**: ۱ روز

### فاز ۲ — «تونل‌زنی BackPack»
**۲.۱ Vendor `third_party/backpack`** (submodule) + NOTICE
**۲.۲ Model `Tunnel` + migrations + CRUD**
**۲.۳ Wizard UI** (Iran/Kharej + transport + link test پیشنهاد)
**۲.۴ Link Test** (latency/jitter/loss → پیشنهاد ترنسپورت) — به‌عنوان action در UI
**۲.۵ Watchdog job** + tunnel-metrics به Prometheus
**۲.۶ PROXY-protocol v2 toggle** + UDP forwarding
**۲.۷ Fallback chain + multi-address failover**
**۲.۸ L3 GRE + Noise + MTU auto** (direct mode)
**تخمین کل**: ۱۰-۱۴ روز

### فاز ۳ — «صفحه‌ی ۳x-ui»
**۳.۱ App icons grid** (۱۰ لوگو + platform detect + deep-links)
**۳.۲ Subscription balancer** (چند اینباند → ۱ sub output + balancer inbound)
**۳.۳ Custom sub page template** (placeholders: {USER},{ROUTE},{REMAINING},{TITLE},... + preview server-rendered)
**۳.۴ External sub import** (+ quota sync)
**۳.۵ Happ/Incy/Salamander URI**
**تخمین**: ۵-۷ روز

### فاز ۴ — «نود advanced + مانیتورینگ»
**۴.۱ Inbound clone to nodes** (sync-mode: all/tags)
**۴.۲ Node tree (transitive)** + ParentGuid
**۴.۳ mTLS bundle + pinned cert SHA256**
**۴.۴ Tunnel health monitor** (probe-through-tunnel + auto-restart, cooldown)
**۴.۵ CPU/Mem history + TG alerts on threshold**
**تخمین**: ۵-۶ روز

### فاز ۵ — «کنترل‌پلین NX-style»
**۵.۱ Roles + limits** (main admin/operator/reseller/custom + max users/device-limit/plan-length)
**۵.۲ Session list/revoke + TOTP + recovery codes** (دو مرحله‌ای در سنجش routes حساس)
**۵.۳ Login ban list + allowlist + trusted_proxies UI**
**۵.۴ Webhooks outbox** (signed + retries + replay log)
**۵.۵ Backup encryption + schedule + pre-restore snapshot**
**۵.۶ Ctrl-K command palette**
**۵.۷ PWA manifest + install prompt**
**تخمین**: ۸-۱۰ روز

### فاز ۶ — «پروتکل‌های عقب‌مانده از 3X-UI»
**۶.۱ Fallbacks (چند پروتکل یک پورت)**
**۶.۲ MTProto per-client quota + fake-tls secrets + live apply**
**۶.۳ Calendar-based client renewal (هفتگی/ماهانه + preview)**
**۶.۴ RESTful API scoped tokens + in-panel reference**
**تخمین**: ۶-۸ روز

---

## ۵. چک‌لیست «بهترین پنل» (معیار پذیرش)

بعد از فاز ۱-۲-۳-۴-۵-۶، پنل ما **باید**:

- [ ] **تونل‌زنی**: ۱۲ ترنسپورت + L3 GRE + link-test + failover + PROXY-v2 + UDP + watch-dog + performance presets
- [ ] **نود**: heartbeat push, metrics (CPU/Mem/Uptime), xray-state, clone inbounds, tree, mTLS, token-encrypted, update from hub, health monitor
- [ ] **سابکریپشن**: app grid, balancers, custom template, external import, HApp/Incy, QR×۳, JSON/Clash/URI
- [ ] **امنیت**: roles + limits, 2FA TOTP + recovery, session mgmt, ban/allowlist, mTLS, backup encrypted
- [ ] **اتوماسیون**: webhooks outbox signed, TG/Discord alerts, scheduled backups, panel self-update
- [ ] **پروتکل**: همه‌ی موجودی فعلی ما (VPN کلاسیک ۹تایی + xray) + fallbacks + MTProto quota
- [ ] **مانیتورینگ**: Prometheus, Grafana, CPU/mem history, link-test, tunnel-metrics, health probes
- [ ] **UX**: Ctrl-K palette, PWA install, RTL+FA, dark theme, wizard برای تونل و نود

---

## ۶. ریسک‌ها

| ریسک | اثر | کاهش |
|---|---|---|
| AGPL در third_party | اگر dependency vendor شود، ما باید کد AGPL تغییرات را منتشر کنیم (ما open GPL هستیم → سازگار) | NOTICE + README + LICENSE-EXCEPTION |
| حجم کد BackPack | ۱۲ ترنسپورت = ~۵۰K سطر | فقط ماژول‌های لازم: tunnel+client+transport+sysstat+quota |
| PCK/xdI فقط Linux+root | panic روی بیلد غیر-لینوکس | `build tag` (لینوکس-only) مثل gso_linux.go |
| Heartbeat push از نود | نود بدون push = فقط poll (legacy) | compat: اگر heartbeat خالی بود → legacy poll |
| Custom sub page | آلودگی صفحه‌ی کاربر با HTML inject | template با placeholder سفید + CSP |
| L3 GRE تونل | نیاز به CAP_NET_ADMIN + kernel | preflight-check و error واضح |

---

## ۷. پیشنهاد شروع

برای همین هفته:

1. **فاز ۱.۱** (heartbeat) — بزرگ‌ترین جهش نسبت به 3X-UI با کمترین ریسک
2. **فاز ۱.۲ + ۱.۳** (update + token-encrypt)
3. **فاز ۲.۱ + ۲.۲ + ۲.۳** (vendor + model + wizard UI) — «تونل زدن مثل BackPack»

بعد:
- فاز ۲.۴-۲.۸ (link-test → watchdog → fallback → L3)
- فاز ۳ (صفحه‌ی 3x-ui)
- فاز ۴-۶ (بقیه)

---

## ۸. منابع کد (نقشه‌ی سریع)

| چیز | مسیر |
|---|---|
| BackPack tunnel engine | `BackPack-main/internal/tunnel/` (bridge, chain, direct, l3, limits, mssclamp, naive, portmap) |
| BackPack transports | `BackPack-main/internal/client/transport/` |
| BackPack managed servers | `BackPack-main/internal/manage/` |
| BackPack link test | `BackPack-main/internal/testport/` + `internal/sysstat` |
| 3X-UI heartbeat | `3x-ui-main/internal/web/job/node_heartbeat_job.go` |
| 3X-UI node metrics | `3x-ui-main/internal/web/service/node.go` + `node_contract.go` |
| 3X-UI sub page | `3x-ui-main/frontend/src/pages/sub/` (SubPage.tsx, SubHero.tsx, SubAppsTab.tsx, SubLinksTab.tsx, SubConfigsTab.tsx) |
| 3X-UI sub balancers | `3x-ui-main/internal/web/service/sub_balancer.go` |
| 3X-UI tunnel monitor | `3x-ui-main/internal/tunnelmonitor/monitor.go` |
| 3X-UI node mTLS | `3x-ui-main/internal/web/service/node_mtls.go` + `setting_mtls.go` |
| NX control plane | `panel-main/` (install.sh + docs; کد مخفی است — فقط الگو) |
| پنل ما: nodes | `vpn-ui-main 3/web/html/nodes.html` + `web/controller/node.go` + `node_api.go` + `web/service/node.go` |
| پنل ما: sub page | `vpn-ui-main 3/web/html/settings/panel/subscription/subpage.html` + `web/assets/js/subscription.js` |
| پنل ما: sub engine | `vpn-ui-main 3/sub/` (sub.go, subService.go, subClashService.go, subJsonService.go, subController.go) |
| پنل ما: tunnel carrier | `vpn-ui-main 3/web/service/vpnoutcarrier.go` + `tunnel-carrier-plan.md` |
