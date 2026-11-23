#!/bin/bash
# Runtime test tailmon di Mac: jalankan server, curl /, /api/devices.json, matikan.
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
cd ~/Development/tailscale-monitor || exit 1
TAILMON_PASSWORD=test ./tailmon > /tmp/tailmon-test.log 2>&1 &
SRVPID=$!
sleep 4
echo "=== / tanpa auth (harap 401) ==="
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8791/
echo "=== /api/devices.json tanpa auth (harap 401) ==="
curl -s -w "\nhttp:%{http_code}\n" http://127.0.0.1:8791/api/devices.json
echo "=== /api/devices.json dengan Bearer ==="
curl -s -H "Authorization: Bearer test" http://127.0.0.1:8791/api/devices.json > /tmp/devices.json
python3 -c "
import json
d = json.load(open('/tmp/devices.json'))
print('keys:', sorted(d.keys()))
devs = d.get('devices') or d.get('nodes') or []
print('device count:', len(devs))
for v in devs:
    print('-', v.get('hostname'), '|', v.get('os'), '|', v.get('tailscale_ip'), '| online:', v.get('online'), '| cpu:', v.get('cpu_model'), v.get('cpu_cores'), '| mem:', v.get('mem_used'), '/', v.get('mem_total'), '| disk:', v.get('disk'))
print('collected_at:', d.get('collected_at'), 'stale:', d.get('stale'))
"
echo "=== / dengan Bearer (harap 200) ==="
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer test" http://127.0.0.1:8791/
kill $SRVPID 2>/dev/null
sleep 1
echo "=== server log ==="
cat /tmp/tailmon-test.log
