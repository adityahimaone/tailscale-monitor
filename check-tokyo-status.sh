#!/bin/bash
# Dijalankan DI Tokyo (via ssh dari Mac). Cek kapasitas + apa yang sudah jalan.
echo "=== uptime/load ==="
uptime
echo "=== mem ==="
free -m
echo "=== disk ==="
df -h / | tail -n +1
echo "=== cgroup limits ==="
cat /sys/fs/cgroup/memory.max 2>/dev/null; cat /sys/fs/cgroup/memory.current 2>/dev/null; cat /sys/fs/cgroup/cpu.max 2>/dev/null
echo "=== docker ==="
which docker || echo "no docker"
docker ps 2>&1 | head -n 10
echo "=== runtimes ==="
for c in node npm go python3 nginx caddy; do printf "%s: " "$c"; which $c 2>/dev/null || echo "no"; done
echo "=== listening ports ==="
(ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null) | head -n 20
echo "=== top procs ==="
ps aux --sort=-%mem | head -n 12
echo "=== os ==="
lsb_release -d 2>/dev/null; uname -a
