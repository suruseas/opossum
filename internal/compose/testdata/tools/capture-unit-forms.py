#!/usr/bin/env python3
"""Capture what docker compose says to a string written for a byte-size key and for
a duration key, for testdata/unit-forms.json (read by unitcast_test.go).

Usage: capture-unit-forms.py > internal/compose/testdata/unit-forms.json

Needs `docker compose` (measured on v5.5.1, Docker 29.8.0). Each value is written
as a double-quoted YAML scalar, so what docker compose sees is exactly the string,
into `shm_size` (a byte size; mem_reservation, memswap_limit and mem_swappiness
were measured to say the same on a sample) and `stop_grace_period` (a duration).
Rows are [value, accepted] where accepted is whether `docker compose config -q`
exited 0.
"""
import json, os, subprocess, sys, tempfile
from concurrent.futures import ThreadPoolExecutor

NUMS = ["0","1","64","1.5","1.",".5","+1","-1","1e3","1E3","1e-3","1_000","1__0","_1","1_","0x10","0b1","0o7","010","1,5","\uff11","1e","e3","--1","+-1","1.2.3","9223372036854775807","9223372036854775808","99999999999999999999","0.0","-0","nan","inf"]
UNITS = ["","b","B","k","K","kb","KB","kB","Kb","m","M","mb","MB","g","G","gb","GB","t","T","tb","TB","p","P","pb","PB","e","E","eb","z","y","ki","Ki","kib","KiB","mi","Mi","gib","GiB","x","bb","kk","byte","bytes","kilobyte"]
BYTES_MISC = ["", " ", " 1g","1g ","1g\n"," 1 g","1 g ","1g1","1 1g","g","kb","b","1 kb ","1\tg","\uff11g","1\uff47","1g,","1g;","(1g)","'1g'","\"1g\"","1g#","1 g b","1G B","1 GiB","1iB","1i","1Ib","0g","-0g","+0g","1.5k","1.5kb","1.g","0.5gb",".5g",
  "1  g","1\tg","1   g","-2","-1.5","-1b","-1k","-2g","-0.5g","-1 g","-1e3","-1_000","-0.0","-0b","+1b","+2","+.5","+.5g","1mib","1MiB","1tib","1pib","1TiB","1PiB","1ib","1eib","1Kib","1kIB","1gIb","1GIB","1e3g","1E3G","1e+3g","1e-3g","1_000g","1_0k","1.5e3","1.5e3m","1e3 kb","0x1g","1e3.5","1.e3","1.e3g",".e3","1_.0","1._0","1_e3","1e_3","1e3_0","1__0g","0_0","0_1","01_0","1.5.5","12345678901234567890123","1e400","1e400g","1e-400","0e0","0E0g","00","000g","1.0","1.0g","1.50gb","1 GB","1 Gb","1 gB","1 gib","1  GiB",
  "1e300g","1e308p","1e300p","1e-300g","9e307p",
  # added later: a negative integer past an int32, hex floats with a p exponent
  "-9223372036854775808","0x1p3","0x1p3g","0x1.8p1","+inf ","-nan ","nan g","inf b","1\r"]
DN = ["0","1","10","1.5","1.",".5","+1","-1","1_0","0x1"]
DU = ["","ns","us","\u00b5s","\u03bcs","ms","s","m","h","d","w","S","MS","Us","H","D","W","min","sec","hour","day","week","y","mo","M","ns ","s "]
DURATION_MISC = ["1h30m","1m30s","1d2h","2h1d","1w1d","1d1w","1h1h","1d1d","1h30","30m1h","1s1ms","1ms1s","1m1","1.5h30m","1h 30m","1h,30m","1h+30m","1h-30m","-1h30m","+1h30m","-1h-30m","1d12h30m15s","1w2d3h4m5s6ms7us8ns","0s","0d","0w","0h0m","1h0m","0m1h",".5d","1.5d","1.d","-1d","+1w","1d ","  1d"," 1d","1D","1d1","d","w","h","s","","-","+",".","1..5s","1.2.3s","1e3s","1e3","9223372036854775807ns","9223372036854775808ns","100000h","1000000000d","99999999999999999999s","-9223372036854775808ns",
  # added later: signs stacked, a dot after a unit, several components that overflow together,
  # the day and the week at their limits, and fractions at the int64 limit (docker compose floors each
  # component's fraction; unitcast.go adds them exactly)
  "+-1s","--1s","-+1s","++1s","1s.5s","106751d23h47m16s854775808ns","106751d23h47m16s854775807ns","106752d","106751d","106752d0s","15251w","15250w","15252w","15250.27w","15250.29w","106751.9d","106752.1d",
  "9223372036854775807.5ns","9223372036854775807.9ns","-9223372036854775807.5ns","-9223372036854775807.9ns",
  "2562047h47m16.8547758075s","2562047h47m16.8547758079999999s","9223372036.8547758079s",
  "4611686018427387903.6ns4611686018427387904.6ns","9223372036854775806.5ns0.6ns","0.9ns9223372036854775807ns",
  "153722867m16.8547758079s","106751d23h47m16.8547758075s",
  "2562047h47m16.85477580799999999999s","9223372036854775807.99999999999999999999ns","1.9999999999999999999999ns9223372036854775806ns"]

def forms_bytes():
    out = set(BYTES_MISC)
    for n in NUMS:
        out.add(n)
        for u in UNITS:
            out.add(n + u)
            out.add(n + " " + u)
    return sorted(out)

def forms_duration():
    out = set(DURATION_MISC)
    for n in DN:
        for u in DU:
            out.add(n + u)
    return sorted(out)

def quote(v):
    return '"' + v.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t") + '"'

def accepted(key, v):
    d = tempfile.mkdtemp(prefix="unit-forms-")
    try:
        with open(d + "/compose.yaml", "w") as f:
            f.write("services:\n  web:\n    image: alpine\n    %s: %s\n" % (key, quote(v)))
        r = subprocess.run(["docker", "compose", "config", "-q"], cwd=d, capture_output=True, text=True, timeout=60)
        return r.returncode == 0
    finally:
        subprocess.run(["rm", "-rf", d])

def capture(key, forms):
    with ThreadPoolExecutor(max_workers=8) as ex:
        return [[v, ok] for v, ok in zip(forms, ex.map(lambda v: accepted(key, v), forms))]

if __name__ == "__main__":
    b, d = forms_bytes(), forms_duration()
    json.dump({"bytes": capture("shm_size", b), "duration": capture("stop_grace_period", d)},
              sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")
