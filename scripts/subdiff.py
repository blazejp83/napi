"""Distort an SRT for sync tests, or compare two SRTs cue by cue.

  subdiff.py distort in.srt out.srt   shift +7.5 s, +3 s more after the midpoint, 25/23.976 speed
  subdiff.py compare a.srt b.srt      start-time differences between matching cues
"""
import re, statistics, sys

TS = re.compile(r'(\d+):(\d\d):(\d\d)[,.](\d{3}) --> (\d+):(\d\d):(\d\d)[,.](\d{3})')

def ms(h, m, s, f): return ((int(h) * 60 + int(m)) * 60 + int(s)) * 1000 + int(f)
def fmt(t):
    t = max(0, int(round(t)))
    return f'{t//3600000:02}:{t//60000%60:02}:{t//1000%60:02},{t%1000:03}'

def read(p):
    return open(p, 'rb').read().decode('utf-8-sig', errors='replace') if p else ''

def cues(text):
    return [(ms(*m.groups()[:4]), ms(*m.groups()[4:])) for m in TS.finditer(text)]

if sys.argv[1] == 'distort':
    text = read(sys.argv[2]).replace('�', '?')
    starts = [c[0] for c in cues(text)]
    mid = starts[len(starts) // 2]
    speed = 25 / 23.976
    def shift(t):
        return t * speed + 7500 + (3000 if t >= mid else 0)
    out = TS.sub(lambda m: fmt(shift(ms(*m.groups()[:4]))) + ' --> ' + fmt(shift(ms(*m.groups()[4:]))), text)
    open(sys.argv[3], 'w', encoding='utf-8', newline='').write(out)
else:
    a, b = cues(read(sys.argv[2])), cues(read(sys.argv[3]))
    n = min(len(a), len(b))
    d = sorted(abs(a[i][0] - b[i][0]) for i in range(n))
    print(f'cues {len(a)} vs {len(b)}; start diff ms: median {statistics.median(d):.0f}, '
          f'p90 {d[int(n*0.9)]:.0f}, p99 {d[int(n*0.99)]:.0f}, max {d[-1]:.0f}; '
          f'within 200 ms: {sum(x <= 200 for x in d) / n:.1%}')
