# گزارش رفع باگ: نمایش «∞ / Unlimited» در ساب‌سکریپشن در حالی که پنل حجم واقعی را نشان می‌دهد

نسخه پنل: vpn-ui 1.9.4 (شاخه‌ی چند-اینباندی / accounts)
تاریخ: 2026-10-10

---

## ۱) خلاصه مشکل کاربر

کاربر یک کلاینت (مثلاً `07311wk8t`) را روی ۶ اینباند ساخته است:

- VLESS :8081، :8082، :8083
- VMess :8084، :8085، :8086

در صفحه‌ی Clients پنل درست نمایش داده می‌شود:

```
0 B / 10.00 GB        30 days after first use
```

اما در صفحه‌ی ساب‌سکریپشن (`/sub/hh787y2bop7tvo3r`):

```
Status:      Unlimited
Total quota: ∞
Expiry:      No expiry
```

در حالی که خود لینک‌های ساب، حجم باقی‌مانده و روزها را نشان می‌دهند
(`Vless-XHTTP-07311wk8t-10.00GB📊-30D⏳`).

همین اتفاق برای اشتراک دوم (`77ufzza6km1gvno3`) هم افتاده: مصرف (`24.48MB`) درست
نمایش داده می‌شود ولی `Total quota: ∞` و `Expiry: No expiry` است، در حالی که
ریمارک لینک `9.98GB📊` و `29D,23H⏳` را نشان می‌دهد.

---

## ۲) ریشه‌ی باگ

ساب‌سکریپشن دو مسیر برای خواندن حجم/انقضا دارد و **هر دو مسیر خراب بودند**:

### مسیر A — مسیر مبتنی بر «accounts» (مسیر جدید و درست)

در `sub/subAccount.go` منطق درستی وجود دارد: هویت (ایمیل) یک بار برای هر پاسخ
resolve می‌شود، حجم و انقضا از جدول `accounts` می‌آید و مصرف از تنها ردیف
`client_traffics` (چون `client_traffics.email` در کل پنل UNIQUE است). هر عضویت
فقط **یک بار** شمرده می‌شود.

اما این مسیر روی یک فلگ در جدول `settings` گیت شده است:

```go
// sub/subAccount.go
scope.migrated = scope.accountService.AccountsMigrated()
```

و `AccountsMigrated()` فقط وقتی true می‌شود که کلید `accountsMigratedAt` نوشته شده
باشد — و این کلید **فقط** در پایان یک «پاس مهاجرت» در
`web/service/migrationaccounts.go` نوشته می‌شود:

```go
needed, totalEntries, err := s.migrationNeeded(db)
...
if !needed {
    return          // ← فلگ هرگز نوشته نمی‌شود
}
```

`migrationNeeded()` تنها یک چیز را می‌پرسد: «آیا تعداد accounts و memberships با
تعداد Client entryها می‌خواند؟»

روی پنلی که از ابتدا (نصب تازه) این شبکه‌ی accounts را با نوشتن‌های زنده‌ی خودش
(`SyncInboundAccounts` که بعد از هر Add/Edit/Delete کلاینت صدا زده می‌شود) هم‌گام
نگه داشته است، جواب این سؤال **همیشه بله** است؛ پس پاس مهاجرت هیچ‌وقت اجرا نمی‌شود
و فلگ `accountsMigratedAt` هرگز ثبت نمی‌شود. نتیجه:

- `AccountsMigrated()` برای همیشه `false` می‌ماند؛
- ساب‌سکریپشن هیچ‌وقت مسیر مبتنی بر accounts را نمی‌خواند؛
- همه‌ی خواننده‌های گیت‌شده (فقط `sub`) تا ابد روی مسیر قدیمی (legacy) می‌مانند.

### مسیر B — مسیر قدیمی (legacy) و باگ اصلی نمایش ∞

در مسیر قدیمی، برای هر اینباند، ردیف ترافیک از preload همان اینباند خوانده می‌شد:

```
membership 1 (اینباندی که ردیف مالک آن است) → {total: 10GB, expiry: ...}
membership 2..6 (بقیه‌ی اینباندها)          → ردیف وجود ندارد ⇒ مقدار صفر
```

و قوانین جمع‌بندی (fold) این‌هاست:

- «هر عضوی که total آن صفر باشد ⇒ کل اشتراک بی‌نهایت می‌شود»
- «اگر انقضای اعضا با هم نخواند ⇒ کل اشتراک No expiry می‌شود»

پس پنج عضو صفر، حساب ۱۰ گیگابایتی را به `∞ / No expiry` تبدیل می‌کردند —
و همین دلیل تفاوت پنل و ساب است: پنل `client_traffics` را می‌خواند، ساب آن را
جمع می‌کرد.

نکته‌ی مهم: در `sub/subService.go` مقدار `exists` برگشتی از `resolveTraffic`
نادیده گرفته می‌شد و صفرِ عضویت‌های بدون ردیف هم به fold اضافه می‌شد:

```go
ct, accountBacked, _ := s.resolveTraffic(inbound, client.Email)   // ← exists دور ریخته می‌شد
usage.add(client.Email, ct, accountBacked)
```

---

## ۳) اصلاحات انجام‌شده

### ۳.۱ `sub/subAccount.go` — resolve کردن هویت به‌صورت سراسری (اصلاح اصلی)

`subScope.traffic` بازنویسی شد و حالا سه لایه دارد:

1. **account** (فقط اگر فلگ مهاجرت ثبت شده باشد): حجم/انقضا از حساب، مصرف از تک‌ردیف `client_traffics`.
2. **تک‌ردیف `client_traffics`** — این بار به‌صورت **سراسری (panel-wide) با ایمیل**، نه از preload اینباند. چون `email` کلید UNIQUE آن جدول است، این همان ردیف حساب است و مستقل از فلگ مهاجرت کار می‌کند.
3. **fallback** — ورودی `settings.clients` همان عضویت، برای حسابی که هنوز هیچ‌وقت شمارش نشده (ردیف ترافیک ندارد).

نتیجه: عضوهایی که ردیف ترافیک ندارند دیگر «صفر» تولید نمی‌کنند؛ همان اعداد
واقعی حساب را برمی‌گردانند. جواب‌های resolve نشده هم cache نمی‌شوند تا یک
فراخوانی بدون اطلاعات، فراخوانی بعدی (که entry دارد) را خراب نکند.

`subUsage.add` هم از «جمع کردن هر عضویت» به «شمردن هر هویت یک بار» تغییر کرد:
چون `client_traffics.email` در کل پنل یکتاست، ایمیل تکراری یعنی همان حساب، و
شمردن چندباره‌اش هم حجم را چند برابر می‌کرد و هم مصرف را. هویت‌های متفاوت زیر
یک subId مثل قبل با هم جمع می‌شوند (رفتار legacy برای چند حساب حفظ شده است).

### ۳.۲ `sub/subService.go` — استفاده از fallback و حذف دور ریختن `exists`

- امضای `resolveTraffic` به `resolveTraffic(inbound, email, fallback *xray.ClientTraffic)` تغییر کرد.
- در `GetSubs`، ورودی تنظیمات همان کلاینت (`entryTraffic(&client)`) به‌عنوان fallback پاس داده می‌شود؛ پس کلاینتی که «هنوز ترافیکی نخورده» هم حجم واقعی‌اش را نشان می‌دهد، نه `∞`.
- `genRemark` با `nil` صدا زده می‌شود (فقط ایمیل در دست است) و رفتار قبلی‌اش حفظ شده است.
- بدون scope (تولید تک‌لینک در تست‌ها/دکمه‌های پنل) همان خواندن preload قبلی باقی ماند.

### ۳.۳ `sub/subJsonService.go` و `sub/subClashService.go`

همان اصلاح، چون این دو هم کپی خودشان از جمع‌بندی را داشتند:
ساب JSON و ساب Clash نیز همین ∞ و No expiry را نشان می‌دادند.

### ۳.۴ `web/service/migrationaccounts.go` — ثبت فلگ مهاجرت (رفع حلقه‌ی منطقی)

فلگ حالا جزئی از «نیاز به مهاجرت» است:

```go
if !needed && s.AccountsMigrated() {
    return
}
if !needed {
    logger.Info("MigrationAccounts - accounts tables already in step but the migrated flag is unset; verifying and recording it")
}
```

یعنی اگر جداول هم‌گام باشند ولی فلگ ثبت نشده باشد، پاس (که کارش *تأیید* و
*ثبت* است) اجرا می‌شود و فلگ نوشته می‌شود. روی پنل‌های سالم این کار یک بار در
استارت اول انجام می‌شود و از آن به بعد سریع برمی‌گردد.

### ۳.۵ `sub/subConfigService.go` — رفع ریس شرایط رقابتی (bug جانبی)

`ConfigFiles` میزبان (host) درخواست را روی **نمونه‌ی مشترک** سرویس می‌نوشت:

```go
s.address = host   // روی نمونه‌ی shared، بدون forResponse
```

چون تولیدکننده‌های لینک همه `s.address` را می‌خوانند، دو مشترک که هم‌زمان
دانلود `.ovpn/.conf` می‌کنند می‌توانستند آدرس یکدیگر را داخل فایل خودشان
بگیرند (و همان مقدار می‌توانست در یک `GetSubs` هم‌زمان تزریق شود). حالا مثل
`GetSubs` درخواست‌محور شده است (`s = s.forResponse()`).

---

## ۴) سایر مواردی که بررسی شد و باگ نبود (یا باگ شناخته‌شده است)

1. **«N days after first use» و نمایش «No expiry» در ساب**
   وقتی انقضا منفی ذخیره می‌شود (یعنی «N روز از اولین استفاده»)، مسیر legacy
   قاعده‌ای قدیمی دارد: مقدار منفی در هدر `expire` صفر گزارش می‌شود (چون مقدار
   منفی در کلاینت‌ها به‌عنوان تاریخ ۱۹۶۹/منقضی تفسیر می‌شود). بنابراین برای
   کلاینتی که هنوز وصل نشده، ساب «No expiry» می‌گوید در حالی که ریمارک لینک
   `-30D⏳` را نشان می‌دهد. این عمدی و مستند است و با این تغییر
   خراب/بهبود داده نشد؛ اگر می‌خواهید تغییر کند بگویید تا رفتار هدر را عوض کنم.

2. **اینباند غیرفعال**: عضویت‌های روی اینباند غیرفعال از ساب حذف می‌شوند
   (طراحی `getInboundsBySubId`).

3. **چند حساب متفاوت زیر یک subId** (روش قدیمی فروش چند سرور): رفتار جمع‌شدن
   حفظ شد.

4. **برچسب Status = Unlimited**: از `totalByte` می‌آید و با این اصلاح درست
   نمایش داده می‌شود.

---

## ۵) تست‌ها

### تست‌های به‌روزشده

- `sub/subService_accounts_test.go`
  - `TestGetSubsIgnoresAccountsBeforeMigrationCompletes`: حالا مقادیر حساب و ردیف
    ترافیک را **عمداً متفاوت** می‌سازد و ثابت می‌کند روی پنل مهاجرت‌نکرده، جواب از
    ردیف `client_traffics` می‌آید (نه از accounts) و مصرف فقط **یک بار** شمرده
    می‌شود. قبلاً همین تست انتظار `0/0` داشت — یعنی خودِ باگ را تثبیت می‌کرد.
  - `TestSubUsageCountsAnAccountOnce` → `TestSubUsageCountsAnIdentityOnce`.
- `web/service/migrationaccounts_test.go`

### تست‌های جدید (رگرسیون)

- `TestGetSubsReportsAnUnmigratedMultiInboundClient` — سناریوی دقیق کاربر: یک
  کلاینت روی ۳ اینباند، ردیف ترافیک فقط روی یکی، بدون accounts و بدون فلگ
  مهاجرت. انتظار: `total=10GB`، `expire` درست، مصرف یک‌بار، و هدر
  `Subscription-Userinfo` صحیح (نه `total=0`).
- `TestGetSubsReportsAnUnmigratedClientWithNoTrafficRowYet` — کلاینت تازه که
  هنوز هیچ ردیف ترافیکی ندارد: حجم/انقضا از `settings.clients` گزارش می‌شود.
- `TestMigrationAccountsRecordsAnAlreadyInStepPanel` — جداول هم‌گام + نبود فلگ ⇒
  `AccountsMigrated()` بعد از اجرا باید true باشد.

### وضعیت اجرای تست‌ها

این مخزن فقط برای لینوکس کامپایل می‌شود (فایل‌هایی مثل `web/service/control.go`
و `web/service/gre.go` از `syscall.Ucred`/`unix.*` استفاده می‌کنند)، و این ماشین
ویندوز است. Go 1.26.2 همین‌جا نصب شد و این نتایج واقعی گرفته شد:

- ✅ `GOOS=linux GOARCH=amd64 go build ./...` → موفق (exit 0)
- ✅ `GOOS=linux GOARCH=amd64 go vet ./sub/... ./web/service/... ./web/controller/...` → موفق (exit 0)
- ✅ کامپایل باینری تست‌ها: `go test -c ./sub/`، `./web/service/`، `./web/controller/` → موفق (exit 0)
- ✅ `gofmt -l` روی همه‌ی فایل‌های تغییریافته → هیچ فایلی نیاز به فرمت‌دهی نداشت
- ❌ **اجرای** تست‌ها روی این هاست ممکن نشد: `go test` نیازمند لینوکس است و هیچ
  محیط لینوکسی روی این ماشین موجود نبود (نه Docker/podman، نه WSL سالم).

> نکته: اولین اجرای بررسی با `GOFLAGS=-mod=mod` انجام شده بود که `go.mod` را
> بازنویسی کرد (جابه‌جایی `github.com/Jipok/wgctrl-go` از بلوک indirect به
> بلوک مستقیم). این فایل **بایت‌به‌بایت** به حالت اصلی برگردانده شد و همه‌ی
> بررسی‌های بعدی در حالت پیش‌فرض (`-mod=readonly`) اجرا شدند و `go.mod` و
> `go.sum` دست‌نخورده ماندند.

### اسکریپت آماده‌ی اجرای تست‌ها روی لینوکس

برای اینکه این تست‌ها واقعاً اجرا شوند، اسکریپت `run-linux-tests.sh` اضافه شد.
این اسکریپت یک توزیع سبک Ubuntu را به‌صورت یک distro اختصاصی WSL وارد می‌کند،
داخل آن `build-essential` (لازم برای cgo و درایور sqlite) و نسخه‌ی Go مورد
نیاز `go.mod` را نصب می‌کند و بعد تست‌ها را اجرا می‌کند:

```bash
./run-linux-tests.sh                        # ./sub/... ./web/service/...
./run-linux-tests.sh ./...                  # تمام تست‌های پروژه
./run-linux-tests.sh -run TestGetSubs ./sub/
```

خروجی در `wsl-go-test.log` ذخیره می‌شود و exit code همان `go test` است.

**پیش‌نیاز:** قابلیت `Microsoft-Windows-Subsystem-Linux` باید فعال و سیستم یک بار
ری‌استارت شده باشد (ویندوز تا قبل از ری‌استارت، `wsl.exe` را با پیام
«The file cannot be accessed by the system» رد می‌کند). این قابلیت روی این سیستم
فعال شد و فقط ری‌استارت مانده است.

---

## ۶) نحوه‌ی اعمال روی سرور و تأیید

1. بیلد/به‌روزرسانی پنل روی سرور.
2. **پنل را یک بار ری‌استارت کنید** تا اصلاح ۳.۴ (ثبت فلگ مهاجرت) اجرا شود.
   در لاگ باید یکی از این دو را ببینید:
   - `MigrationAccounts - N accounts and M memberships backfilled ...` (پاس کامل)
   - `MigrationAccounts - accounts tables already in step but the migrated flag is unset; verifying and recording it`
3. تست فلگ:

```sql
SELECT key, value FROM settings WHERE key IN ('accountsMigratedAt','accountsMigrationReport');
```

اگر `accountsMigratedAt` مقدار دارد، مسیر accounts فعال است.

4. صفحه‌ی ساب را باز کنید؛ باید `Total quota: 10.00GB` و `Status: Active` شود
   (برای کلاینتی که انقضایش مثبت است، تاریخ انقضا هم می‌آید).
5. تست هدر برای کلاینت:

```bash
curl -sI "http://<host>:2097/sub/<subId>" | grep -i subscription-userinfo
# انتظار: total=10737418240 (۱۰ گیگ) و expire=<timestamp>
```

نکته: اصلاح ۳.۱/۳.۲ مستقل از ری‌استارت است و از همان لحظه‌ی بیلد جدید، حتی روی
پنل مهاجرت‌نکرده، نمایش صحیح را برمی‌گرداند؛ ری‌استارت فقط مسیر
حساب‌محور (authoritative) را فعال می‌کند.

---

## ۷) فایل‌های تغییر‌یافته

| فایل | تغییر |
| --- | --- |
| `sub/subAccount.go` | resolve هویت سراسری (account → ردیف ترافیک سراسری → ورودی تنظیمات) + شمردن هر هویت یک بار |
| `sub/subService.go` | امضای `resolveTraffic` با fallback، `entryTraffic`، استفاده در `GetSubs`/`genRemark` |
| `sub/subJsonService.go` | همان اصلاح برای ساب JSON |
| `sub/subClashService.go` | همان اصلاح برای ساب Clash |
| `sub/subConfigService.go` | درخواست‌محور کردن `ConfigFiles` (رفع ریس) |
| `web/service/migrationaccounts.go` | ثبت فلگ `accountsMigratedAt` وقتی جداول از قبل هم‌گام‌اند |
| `sub/subService_accounts_test.go` | به‌روزرسانی تست‌های قدیمی + ۲ تست رگرسیون |
| `web/service/migrationaccounts_test.go` | تست ثبت فلگ روی پنل هم‌گام |
| `run-linux-tests.sh` | اسکریپت اجرای سوئیت تست روی لینوکس (وارد کردن distro اختصاصی WSL) |
