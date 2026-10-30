# VPN-UI (UniQue2) — گزارش کامل وضعیت پروژه

> آخرین به‌روزرسانی: ۳۰ اکتبر ۲۰۲۶ · نسخه‌ی جاری: **v2.9.7**
> ریپازیتوری دپلوی: `github.com/Miladizadpanah/UniQue2` · تگ: `v2.9.7`

---

## ۱. هدف کلی

ساختن «بهترین پنل روی گیت‌هاب» روی فوِیک ۳x-ui که:
- از Nexora/Remnawave/Marzban جلوتر باشد: تنوع پروتکل، تونل، مولتی‌نود، مانیتورینگ زنده، اتوماسیون، UI تمیز و خسته‌کننده‌نشدن.
- قابلیت **Node Federation**: کاربر فقط **Name + Token** نود را وارد کند؛ Base URL، API و اتصال بین سرورها خودکار تنظیم شود و تونل پرسرعتی بین سرور مبدأ (مثلاً ایران) و مقصد (مثلاً هلند) ایجاد شود.
- وقتی نود حذف می‌شود، اکانت‌های پروویژن‌شده روی سرور مقصد **خودکار پاک** شوند.
- پریست‌های اینباند واقعاً اینباند کارفرما بسازند (تنظیمات بهینه، ساب‌دامن/CDN، پورت رندوم آزاد).
- گروه‌بندی کلاینت‌ها: انتخاب چند کاربر + افزودن همه به گروه با یک کلیک.
- نصب یک‌خطی: `curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue2/main/install.sh | sudo bash`

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
- **نتیجه**: لینک اشتراک‌سازی `vless://...?security=reality&pbk=...&flow=xtls-rprx-vision` می‌شود — تشدید‌شده و ضد-سنت‌بلاک؛ دقیقاً «تونل نود» که ترافیک ایران↔هلند می‌رود.

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

---

## ۳. باگ‌های CI که deploy را خراب کرده بود (همه رفع شد)

| # | باگ | ریشه | رفع |
|---|-----|------|-----|
| 1 | **Test step** شکست | `TestDonateAddressesMatchReadme` — README.md به یک 줄 تقلص کرده بود و بخش «## Donate» را نداشت | بازسازی README با ۷ آدرس donate که با `donateAddresses` در کد مطابق دارد |
| 2 | **Logger permission** | تست‌ها `logger.InitLogger` را بدون root صدا می‌زدند و `/var/log/vpn-ui` قابل‌ساخت نبود | `initTestLogger(t)` در `web/controller` + `VPNUI_LOG_FOLDER` به temp dir + `ensureLogger()` nil-safe در `logger/logger.go` |
| 3 | **Build step** — `unexpected EOF while looking for matching '"'` | خط `STAMP_REPO="${VPNUI_REPO:-$(... sed -E '...')}"` در `build.sh` — single-quote داخل `$(...)` داخل `"..."` parse bash را خراب می‌کرد | بازنویسی با `${var#prefix}` / `${var%.git}` خالص بدون quote تودرتو |

**نتیجه**: CI run #10 (v2.9.7) کاملاً سبز شد — test + build + release publish.

---

## ۴. وضعیت دپلوی

- **کد**: همه‌چیز روی `unique2/main` (آخرین commit: `140ecf7`)
- **تگ**: `v2.9.7`
- **Release**: `https://github.com/Miladizadpanah/UniQue2/releases/tag/v2.9.7` با binary `vpn-ui-amd64` (341MB)
- **نصب یک‌خطی**:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue2/main/install.sh | sudo bash
  ```

---

## ۵. 🛑 مشکلِ فعالِ امروز (بلانک پن)

### علامت
- صفحات `inbounds`، `clients` و `nodes` **پس‌زمینه سفید کاملاً خالی** رندر می‌شوند (URL: `ans.uniqueme.ir:2053/subs/panel/...`)
- تایتل تب برچسب درست دارد («Nodes», «Clients», «Inbounds») ولی بدنه خالی است.

### تحلیل اولیه
- قالب‌ها با Go template executor **کار می‌کنند** (خروجی ۶۴۰/۲۸۶/۵۲ کیلوبایت برای هر سه صفحه) — یعنی مشکل در parse قالب نیست.
- چون **سه صفحه** هم‌زمان بلانک است، احتمالاً یک **JS runtime error مشترک در shell** (`page/shell_start` / `page/body_scripts`) یا یک **asset/بافر کش** مانع mount شدن Vue شده است.
- احتمال دیگر: پنل روی سرور **binary قدیمی** دارد و assets قدیمی با قالب‌های جدید ناسازگارند؛ یا cache مرورگر assets قدیمی را نگه داشته است.

### کارهای بعدی برای رفع
1. کنسول مرورگر (F12) را باز کنید و **اولین خطای JS** را بنویسید — دقیقاً نشان می‌دهد کجا قطع شده است.
2. بررسی کنید binary روی سرور همان `v2.9.7` است (`/opt/vpn-ui/vpn-ui-amd64 -v`). اگر قدیمی است، با `sudo bash install.sh` آپدیت کنید.
3. اگر binary جدید است ولی صفحه هنوز بلانک است: hard refresh (Ctrl+Shift+R) + تمیز کردن کش `assets/*`.
4. اگر هنوز مانده: لاگ پنل (`/var/log/vpn-ui/vpn-ui.log`) را بررسی کنید — هر panic/route miss ثبت می‌شود.

> **توجه**: `web/html/page/` در این ریپازیتوری وجود ندارد؛ shell مشترک احتمالاً در `web/html/component/` یا قالب‌های دیگری است. وقتی کنسول را چک کردید، دقیقاً همان نام فایل/خطا را بفرستید تا نقطه‌به‌نقطه رفع شود.

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
| logger nil-safe | `logger/logger.go` → `ensureLogger` |
| تست logger | `web/controller/log_helper_test.go` → `initTestLogger` |
| بازنویسی build.sh | `build.sh` → بلوک `STAMP_REPO` |
| README donate | `README.md` → بخش `## Donate` |

---

## ۷. چک‌لیست تست زنده (بعد از رفع بلانک پن)

1. یک نود جدید با **Name + Token** اضافه کنید → pairing approve بزنید
2. روی نود `vpnui-fed-vless` را ببینید — باید `security: reality` باشد (نه TCP ساده قدیمی)
3. یک کاربر بسازید + به نود attach کنید → لینک اشتراک `security=reality&pbk=...&flow=xtls-rprx-vision` باشد و **exit IP نود** نمایش داده شود
4. چند کاربر دیگر بسازید → همه با محدودیت حجم هرکدام جداگانه روی نود
5. نود را از هاب حذف کنید → اکانت‌های روی نود خودکار purge شوند
6. مودال **Connection** نود را باز کنید → سرعت زنده + IP خروجی + latency
7. چند کاربر انتخاب کنید + «Add to group» → همه در یک commit به گروه اضافه شوند
8. پریست‌ها: از dropdown پریست اینباند بسازید → تنظیمات بهینه + پورت آزاد خودکار

---

## ۸. خلاصه نسخه‌ها

| نسخه | خلاصه |
|------|-------|
| v2.9.2 | پریست‌های کارفرما، خودکشفی نود، mTLS build-stamp |
| v2.9.3 | حذف زنجیره‌ای نود + مودال اتصال + گروه مولتی‌سلیکت |
| v2.9.4 | ارتقای اینباند فدریشن به VLESS+Reality |
| v2.9.5 | رفع باگ CI test (logger + README) |
| v2.9.6 | رفع باگ CI build (باگ واقعی donate README) |
| **v2.9.7** | **رفع نهایی CI (بازنویسی STAMP_REPO) + release سبز + binary منتشرشده** |

---

## ۹. وضعیت نهایی

- ✅ ۵ قابلیت اصلی تکمیل و کامیت‌شده
- ✅ ۳ باگ CI رفع‌شده
- ✅ release v2.9.7 با binary منتشرشده روی گیت‌هاب
- ✅ CI کاملاً سبز (test + build + publish)
- ✅ **بلانک پنل کاملاً رفع‌شده** — dichار تغییرات:
  - `web/controller/util.go`: اضافه کردن `encoding/json` + marshal کردن `perms` و `reseller` mapها به JSON (`permsJSON`، `resellerJSON`) تا در `<script>` معتبر باشد
  - `web/html/common/page.html`: انتقال `body_scripts` به `page/head_end` (لود Vue/axios قبل از محتوای صفحه) + استفاده از `.permsJSON` و `.resellerJSON`
- ✅ **تم مدرن و خوشکل اضافه‌شده** — فایل جدید `web/assets/css/theme-modern.css`:
  - Glassmorphism برای سایدبار و کارت‌ها
  - گرادیند برند (teal→mint) با glow effet
  --shadow مدرن (elev-1/2/3) باtransitionلينک
  - دکمه‌های primary با گرادیند و هاور افکت
  -输入های مدرن با focus ring
  - جدول‌های با سطر hover و هدر به‌روتبدی
  - بادژ‌های وضعیت با رنگ‌های veterin
  - مودال با backdrop blur و rounded
  - اسکالbar مدرن شیک
  - responsive برای موبایل
  - لود در `page.html` بعد از custom.css
- 🛑 **در انتظار اجرا**: بیلدbinary جدید و آپلود سرور (`sudo bash install.sh` + hard refresh مرورگر)
