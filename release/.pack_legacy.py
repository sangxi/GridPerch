"""规范化兼容包文本文件、重算 SHA256SUMS.txt、打成 zip。

CRLF 的原因：Windows 7 / Server 2008 R2 自带记事本只认 CRLF，纯 LF 的 txt
会显示成挤在一行；老 cmd.exe 解析 bat 对 LF 也不可靠。

文件名全 ASCII 的原因：Win7 / 2008 R2 自带的 zip 解压不认 UTF-8 文件名标志位，
中文名会变成乱码，所以说明文件用 README.txt。
"""

import glob
import hashlib
import os
import zipfile

BOM = b"\xef\xbb\xbf"
TEXT_FILES = ["install.ps1", "install.bat", "uninstall.bat", "README.txt"]

for name in TEXT_FILES:
    raw = open(name, "rb").read()
    has_bom = raw.startswith(BOM)
    body = raw[len(BOM):] if has_bom else raw
    body = body.replace(b"\r\n", b"\n").replace(b"\r", b"\n").replace(b"\n", b"\r\n")
    with open(name, "wb") as f:
        if has_bom:
            f.write(BOM)
        f.write(body)
    print("CRLF: %-22s bom=%s" % (name, has_bom))

lines = []
for fn in sorted(glob.glob("*")):
    if fn == "SHA256SUMS.txt" or os.path.isdir(fn):
        continue
    h = hashlib.sha256(open(fn, "rb").read()).hexdigest()
    lines.append("%s  %s" % (h, fn))
    print("%s  %s" % (h, fn))

with open("SHA256SUMS.txt", "w", newline="\r\n", encoding="utf-8") as f:
    f.write("\n".join(lines) + "\n")
print("SHA256SUMS.txt entries:", len(lines))

TOP = "gridperch-winlegacy-1.2.6"
out = os.path.join("..", TOP + ".zip")
names = sorted(glob.glob("*"))
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for fn in names:
        if os.path.isdir(fn):
            continue
        zf.write(fn, TOP + "/" + fn)
print("ZIP written:", os.path.abspath(out), os.path.getsize(out), "bytes")
