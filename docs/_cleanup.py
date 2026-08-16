# -*- coding: utf-8 -*-
"""一次性清理脚本：删除三份文档中失控生成的段落（按已核对的 1-based 行号边界）。"""
import io

BASE = r"f:/PersonalProject-Demetrius/NimbusDrive/docs/"

JOBS = [
    # (文件, 保留区间列表) —— 其余行全部删除
    ("功能文档_v1.md",   [(1, 385), (652, 10**9)]),   # 删 3.2.3~3.2.40 重复/嵌套段
    ("实现方案_v1.md",   [(1, 732), (787, 810)]),     # 删 /range/final 与缩略图嵌套变体，保留 /thumbnail
    ("接口文档_v1.md",   [(1, 1696)]),                # 删 2.6.8~2.6.27 缩略图嵌套段
]

for name, ranges in JOBS:
    path = BASE + name
    with io.open(path, "r", encoding="utf-8", newline="") as f:
        lines = f.readlines()
    total = len(lines)
    keep = []
    for lo, hi in ranges:
        keep.extend(lines[lo - 1 : hi])  # 1-based -> 0-based
    with io.open(path, "w", encoding="utf-8", newline="") as f:
        f.writelines(keep)
    print("%s: %d -> %d lines (removed %d)" % (name, total, len(keep), total - len(keep)))
