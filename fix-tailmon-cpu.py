#!/usr/bin/env python3
# Fix CPU_MODEL parsing: /proc/cpuinfo has BOTH "model : 1" (numeric, comes
# first) and "model name : AMD EPYC ..." — the old awk took the first "model"
# match, so cpu_model became "1". Prefer "model name", fall back to "model".
p = '/Users/adityahimawan/Development/tailscale-monitor/main.go'
s = open(p).read()
old = """  /^[Mm]odel name[[:space:]]*:/ && m=="" { gsub(/^[[:space:]]+/, "", $2); m=$2 }
  /^[Mm]odel[[:space:]]*:/ && m=="" { gsub(/^[[:space:]]+/, "", $2); m=$2 }
  /^[Hh]ardware[[:space:]]*:/ && h=="" { gsub(/^[[:space:]]+/, "", $2); h=$2 }
  END { if (m != "") print m; else if (h != "") print h }"""
new = """  /^[Mm]odel name[[:space:]]*:/ && m=="" { gsub(/^[[:space:]]+/, "", $2); m=$2 }
  /^[Mm]odel[[:space:]]*:/ && o=="" { gsub(/^[[:space:]]+/, "", $2); o=$2 }
  /^[Hh]ardware[[:space:]]*:/ && h=="" { gsub(/^[[:space:]]+/, "", $2); h=$2 }
  END { if (m != "") print m; else if (o != "") print o; else if (h != "") print h }"""
assert old in s, "pattern not found in main.go"
open(p, 'w').write(s.replace(old, new))
print("patched OK")
