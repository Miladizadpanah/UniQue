# VPN-UI (UniQue)

The next-generation 3x-ui panel: multi-node federation, node-hosted inbounds, client groups,
inbound presets, live monitoring and security automation. One-line install:

```bash
curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash
```

Update an existing panel from inside the UI (Settings → Update), or re-run the same
command with `VPNUI_REF=<tag>` to pin a release.

## Features

- **Node federation** — add a node with just a Name + Token; the Base URL, API
  connection and a fast encrypted tunnel between the two servers are configured
  automatically. Node-hosted inbounds appear in your subscriptions and show the
  node's exit IP; deleting a node on the hub automatically purges the accounts it
  provisioned on the destination.
- **VLESS + Reality federation inbounds** — the dedicated inbound created on each
  node ships with a fresh x25519 key pair, multi-SNI camouflage and XTLS Vision
  on every account: the strongest censorship resistance the panel offers.
- **Client groups** — pick several clients at once and add them all to a group in
  one commit; a group is a saved view, so dropping it never loses a client.
- **Inbound presets** — VLESS+REALITY, Hysteria2, VLESS+TLS, TUIC v5, NaiveProxy
  and Shadowsocks-2022, each created through the panel's own inbound path with a
  kernel-checked free port.
- **Live monitoring** — per-node connection status, exit IP, latency and up/down
  speed; Prometheus metrics at `/panel/metrics` plus a Grafana fleet dashboard
  (`monitoring/grafana/vpnui-fleet.json`); webhooks for node online/offline,
  disk and memory events.
- **Security** — node pairing gate (an operator must approve each hub), mTLS CA
  generated automatically on every boot, anti-flapping offline detection.

## Donate

🔹USDC-Polygon: `0xdC2Ab962954e8fA1502C44656c5A32CF2979568C`

🔹USDT-BEP20: `0xdC2Ab962954e8fA1502C44656c5A32CF2979568C`

🔹USDT-TRC20: `TXEhckDXtdLGAjP5PZXfNnQjPHzEVTcBmR`

🔹TRX: `TXEhckDXtdLGAjP5PZXfNnQjPHzEVTcBmR`

🔹LTC: `ltc1qmapmnuf6cq9x679nmu0k4uyq779mxxcwnkgdll`

🔹BTC: `bc1q62w7lyndzndsp74vj4dsayvun8xnapzq6hx5ea`

🔹ETH: `0xdC2Ab962954e8fA1502C44656c5A32CF2979568C`
