import os
import subprocess

EXTS = {'.go', '.js', '.html', '.css', '.md', '.sh', '.bat', '.yml', '.yaml',
        '.ps1', '.mod', '.sum', '.txt', '.env', '.json', '.example', '.service'}
SKIP_DIR = ('dist/', 'release/', 'bin/', '.git/', '.workbuddy/')

files = subprocess.check_output(['git', 'ls-files'], text=True).splitlines()
targets = []
for f in files:
    f2 = f.replace(os.sep, '/')
    if any(f2.startswith(d) for d in SKIP_DIR):
        continue
    base = os.path.basename(f2)
    if base.startswith('Dockerfile') or base == '.gitattributes':
        targets.append(f2)
        continue
    if os.path.splitext(f2)[1].lower() in EXTS:
        targets.append(f2)

RULES = [('STRIX2-', 'GRIDPERCH2-'), ('STRIX_', 'GRIDPERCH_'), ('STRIX', 'GRIDPERCH'),
         ('Strix', 'GridPerch'), ('strix', 'gridperch')]

changed = []
for f in targets:
    if not os.path.exists(f):
        continue
    with open(f, 'r', encoding='utf-8', newline='') as fh:
        s = fh.read()
    orig = s
    for a, b in RULES:
        s = s.replace(a, b)
    if s != orig:
        with open(f, 'w', encoding='utf-8', newline='') as fh:
            fh.write(s)
        changed.append(f)

print('scanned %d text files, changed %d' % (len(targets), len(changed)))
for f in changed:
    print('  ', f)
