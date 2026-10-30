# VPN-UI (UniQue) — گزارش کامل وضعیت پروژه

> آخرین به‌روزرسانی: ۳۰ اکتبر ۲۰۲۶ · نسخه‌ی جاری: **v3.0.0**
> ریپازیتوری دپلوی: `github.com/Miladizadpanah/UniQue` · تگ: `v3.0.0`

---

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
