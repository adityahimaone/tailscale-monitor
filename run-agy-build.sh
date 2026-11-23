#!/bin/bash
# Build tailscale-monitor di Mac pakai agy CLI, model Orchestrator (Argon).
# Dijalankan lewat SSH (VPS -> Mac). Output juga ke agy-run.log via tee di caller.
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
cd ~/Development/tailscale-monitor || exit 1

PROMPT=$(cat <<'PROMPT_EOF'
Baca file PLAN.md di folder ini sampai paham, lalu bangun aplikasinya sampai selesai.

Yang harus dibuat di folder ini:
1. main.go + go.mod — collector + API server Go, STDLIB SAJA, tanpa dependensi eksternal.
   - Listen 127.0.0.1:8791, serve index.html di / dan JSON di GET /api/devices.json.
   - Tiap 60 detik poll `tailscale status --json`, cache hasilnya; API selalu serve cache terakhir plus field collected_at dan stale (true kalau poll terakhir gagal).
   - Untuk tiap node online, coba ambil spec via SSH dengan timeout max 5 detik per node, semua node paralel; gagalnya satu node TIDAK BOLEH menggagalkan node lain atau API.
   - Target SSH configurable via env, default untuk jalan di VPS: TAILMON_SSH_MAC="mac-tailscale", TAILMON_SSH_TOKYO="ssh -p 25528 root@jp-tyo01.tryaltare.ai". Node sendiri (tempat collector jalan) pakai perintah lokal. Node Windows/Android/offline/tanpa SSH tampil hanya dengan data Tailscale, spec "n/a".
   - Field per device: hostname, dns_name, os, os_version, tailscale_ip, online, active, relay, last_seen, key_expiry, tx_bytes, rx_bytes, lalu kalau ada: cpu_model, cpu_cores, load_avg, mem_total, mem_used, disk (array mount: target, total, used), uptime_seconds.
   - Password gate: kalau env TAILMON_PASSWORD diset (non-empty), semua route minta password itu dulu (cek header Authorization Bearer ATAU cookie/query sederhana — pilih satu cara yang simpel dan konsisten, dokumentasikan di README). Jangan pernah hardcode password di repo.
2. index.html — SATU file statis, vanilla JS + CSS saja, tanpa framework, tanpa build step. Dark UI, kartu per device, status online/offline jelas, sort online dulu, search/filter teks, auto-refresh 60 detik, bar untuk RAM dan disk, warning kalau key_expiry < 14 hari, tampilkan collected_at + stale kalau ada.
3. README.md — cara build, cara jalanin di VPS, cara pasang nginx reverse proxy ke 127.0.0.1:8791 untuk devices.adityahimaone.space, cara set TAILMON_PASSWORD, dan apa yang masih perlu dilakukan saat deploy.

Verifikasi sebelum selesai (lakukan semuanya, jangan cuma klaim):
- gofmt -l harus kosong, go vet lolos, go build -o tailmon . berhasil.
- Jalankan ./tailmon dengan TAILMON_PASSWORD=test sebentar, curl http://127.0.0.1:8791/api/devices.json dan pastikan JSON valid dan berisi 9 device; curl halaman / dan pastikan HTML ter-serve. Lalu matikan servernya.
- Catatan: verifikasi ini jalan di Mac — spec node remote (VPS/Tokyo) dari Mac kemungkinan akan "n/a" karena target SSH default-nya untuk VPS; itu WAJAR dan bukan kegagalan. Yang penting: 9 device tampil dan spec Mac sendiri (localhost) terisi.
- Jangan install dependensi eksternal, jangan commit secret/password/key apa pun, jangan ubah file PLAN.md.

Terakhir, laporkan dengan jelas: file apa saja yang dibuat/diubah, hasil go vet/build, hasil curl (jumlah device + contoh 1 device lengkap), dan apa yang belum terverifikasi (deploy VPS + nginx + DNS dilakukan terpisah, bukan olehmu).
PROMPT_EOF
)

~/.local/bin/agy --model gemini-pro-orchestrator-g1-pro --mode auto --dangerously-skip-permissions --print-timeout 0 -p="$PROMPT"
echo "AGY_EXIT:$?"
