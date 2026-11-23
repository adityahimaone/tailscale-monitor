#!/bin/bash
# Tunggu agy build tailscale-monitor selesai di Mac (max ~10 menit),
# lalu tulis ringkasan ke agy-wait-result.txt. Dijalankan lewat SSH.
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
cd ~/Development/tailscale-monitor || exit 1
for i in $(seq 1 40); do
  if ! pgrep -f "agy --mode[l] auto" >/dev/null 2>&1; then
    break
  fi
  sleep 15
done
{
  echo "=== WAIT DONE $(date) ==="
  echo "--- files ---"
  ls -la
  echo "--- agy-run.log tail ---"
  tail -n 100 agy-run.log
  echo "--- agy CLI still running? ---"
  pgrep -fl "agy --mode[l] auto" || echo "agy CLI not running"
} | tee agy-wait-result.txt
