# Tailscale Monitor — Build Brief (untuk agy, model Orchestrator Argon)

Tujuan: web app ringan buat monitoring semua device di tailnet Aditya,
dengan spec lengkap per device (CPU, RAM, storage, uptime), bukan cuma
status online/offline. Nanti dipasang di subdomain `devices.adityahimaone.space`
(subdomain: devices.adityahimaone.space).

## Fakta live (dicek 23 Nov 2026 ~17:03 WIB, via `tailscale status --json` di VPS)

Tailnet: `adityahimaone.github`, Magic DNS suffix `tail19bf7d.ts.net`.
9 device, 4 online:

| Host | OS | Tailscale IP | Status | Relay | Last seen | Key expiry |
|---|---|---|---|---|---|---|
| Aditya's MacBook Pro | macOS | 100.75.2.78 | online, active | sin | — | **2026-12-02** (SEGERA EXPIRED — tampilkan warning di UI) |
| VM-0-16-ubuntu (VPS) | linux | 100.80.220.71 | online | sin | — | 2027-04-24 |
| muse-1 | (tanpa OS) | 100.114.248.97 | online, active | nyc | 22 Nov | 2027-05-09 |
| tokyo-ubuntu | linux | 100.112.218.31 | online | tok | — | 2027-05-22 |
| aditya-rtx-4070-super | windows | 100.122.101.28 | offline | sin | 19 Nov 2026 | 2027-04-10 |
| Aditya S24 FE | android | 100.107.160.53 | offline | sin | 29 Okt 2026 | 2027-04-27 |
| instabox-vps-1 | linux | 100.108.18.9 | offline | sea | 15 Nov 2026 | 2027-05-05 |
| muse-2 | (tanpa OS) | 100.79.41.92 | offline | nyc | 23 Nov 2026 09:50 | 2027-05-09 |
| openbot-hermes | linux | 100.100.130.90 | offline | sea | 6 Nov 2026 | 2027-05-05 |

## Arsitektur (ringan, tanpa framework, tanpa build step)

- Frontend: SATU file `index.html` statis, vanilla JS + CSS saja. Tanpa
  npm, tanpa bundler, tanpa framework. Dark UI, kartu per device, auto-refresh
  60 detik, filter/search, sort online dulu. Tampilkan: status dot,
  hostname, OS, Tailscale IP + Magic DNS, relay, last seen, key expiry
  (warning kalau < 14 hari), traffic tx/rx, dan kalau ada datanya:
  CPU model + cores + load, RAM terpakai/total + bar, disk terpakai/total
  + bar per mount utama, uptime, OS version.
- Collector/API: SATU binary Go (stdlib saja, tanpa dependensi eksternal),
  mengikuti pola Switchyard/kanban di VPS. Jalan di VPS (yang selalu nyala),
  listen di `127.0.0.1:8791`, serve `index.html` + `GET /api/devices.json`.
  Poll tiap 60 detik, cache hasilnya, API selalu serve cache terakhir +
  `collected_at` + `stale` flag kalau poll terakhir gagal.
- JANGAN pakai Tailscale API key — tidak perlu. List device dari
  `tailscale status --json` yang dijalankan lokal di VPS.

## Cara collector mengambil spec (Tailscale API TIDAK punya data ini)

- List device: `tailscale status --json` di VPS.
- VPS sendiri: perintah lokal langsung.
- Mac: dari VPS `ssh mac-tailscale` (alias sudah ada di `~/.ssh/config`
  VPS, user `adityahimawan`, host `100.75.2.78`).
- Tokyo: SSH tailnet DITOLAK (container userspace mode, port 22 refused).
  Pakai SSH publik `ssh -p 25528 root@jp-tyo01.tryaltare.ai` dengan key
  yang sudah ada. Kalau dari VPS tidak bisa, tandai spec Tokyo
  "unavailable" — jangan bikin collector hang/crash karenanya.
- Windows / Android / node offline / muse-1 / muse-2: tidak ada SSH —
  tampilkan hanya data Tailscale-nya, spec bertanda "n/a".
- Setiap SSH poll harus punya timeout singkat (max ~5 detik per node),
  dijalankan paralel, dan kegagalan satu node TIDAK BOLEH menggagalkan
  node lain atau API. Jangan pernah block API lebih dari beberapa detik.

### Perintah spec

- Linux: `nproc`, `grep 'model name' /proc/cpuinfo | head -1`,
  `uptime`, `free -b`, `df -B1 --output=target,size,used,avail /`,
  `lsb_release -d` / `/etc/os-release`, `cat /proc/loadavg`.
  Ingat: di container (Tokyo) angka `free`/`df`/`nproc` adalah spek HOST —
  tampilkan apa adanya, tapi beri label "host values" untuk Tokyo.
  VPS: 2 vCPU, 1.9 GiB RAM, disk 40G (92% per 23 Nov — tampilkan jujur).
- macOS: `sysctl -n machdep.cpu.brand_string`, `sysctl hw.ncpu`,
  `sysctl hw.memsize`, `vm_stat` / `memory_pressure`, `df -k /`,
  `uptime`, `sw_vers`. Mac: Apple M1, 8 GB RAM, macOS 26.5.2.

## Keamanan — WAJIB

- Dashboard TIDAK BOLEH terbuka publik tanpa auth: Tailscale IP dan spec
  server adalah info sensitif. Pakai password gate sederhana (satu shared
  password, pola yang sama dengan kanban workspace): password dari
  environment variable `TAILMON_PASSWORD`, JANGAN hardcode password di repo.
- Jangan simpan API key, token, password, atau private key di repo.
- Jangan commit file `.env` berisi secret.

## Deploy (nanti, setelah user pilih subdomain)

- DNS subdomain `devices.adityahimaone.space` → VPS, nginx reverse proxy ke
  `127.0.0.1:8791` (pola yang sama dengan `kanban.adityahimaone.space`).
- Repo: `~/Development/tailscale-monitor` di Mac. Struktur minimal:
  `main.go`, `index.html`, `README.md` (cara jalanin collector di VPS +
  cara pasang nginx + cara set password), `PLAN.md` (file ini).

## Kriteria selesai

1. `go vet` + `go build` lolos di Mac; binary jalan di VPS.
2. Buka `http://127.0.0.1:8791/` di VPS: semua 9 device tampil, 4 online
   berstatus benar, spec VPS + Mac + Tokyo terisi (atau bertanda jelas
   kalau satu node gagal di-poll), node offline tampil tanpa spec.
3. `GET /api/devices.json` mengembalikan JSON valid dengan semua field di atas.
4. Password gate aktif: tanpa password tidak bisa lihat data.
5. Total repo kecil, tanpa `node_modules`, tanpa dependensi eksternal.
