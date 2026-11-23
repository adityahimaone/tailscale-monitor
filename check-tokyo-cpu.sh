#!/bin/bash
# Cek langsung isi /proc/cpuinfo di Tokyo (untuk debug CPU_MODEL=1).
ssh -o ConnectTimeout=10 -o BatchMode=yes -p 25528 root@jp-tyo01.tryaltare.ai "grep -m3 -i 'model' /proc/cpuinfo; echo ---; uname -m; nproc; echo ---; head -n 5 /proc/cpuinfo"
