# Progress Log

## Session: 2026-10-31
- باگ‌های پریست اینباند شناسایی و رفع شد:
  - VLESS+TLS → blob درست TLS (Security field)
  - Hysteria2 → network:hysteria + hysteriaSettings + ALPN h3
  - TUIC → network:tuic + ALPN h3
  - Naive → security:tls + masquerade
  - Shadowsocks 2022 → method+key+network+clients
  - realitySettings → settings ذیلی (publicKey, fingerprint, spiderX)
- فرم مودال پریست با SNI/CDN/Port اضافه شد (`web/html/inbounds.html`)
- API `addPreset` فیلدهای `sni`, `cdn`, `port` را قبول می‌کند
- `ApplyPresetOverrides` در `web/service/inbound_presets.go` اضافه شد
- کلیدهای i18n جدید در `translate.en_US.toml` + `translate.fa_IR.toml` + ۱۱ لوکال دیگر
- TESTS: ۱۰ تست blob shape در `web/service/inbound_presets_test.go`
- BUILD: `GOOS=linux go build ./...` سبز (exit 0)
- TOML: quote balance check سبز (13 فایل)
- VERSION: 2.8.0 → 3.1.0
- PROJECT_STATUS.md: بخش جدید ۲.۰ + v3.1.0 در خلاصه نسخه‌ها
