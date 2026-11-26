#!/usr/bin/env python3
"""Report code files above a line-count threshold.

Non-blocking inventory/audit tool. >THRESHOLD LOC is a review signal, not a
forced cut target: cohesive files may remain larger when they own a clear
context/domain. Split only along clear responsibility/context boundaries and
never change behavior/contracts/domain ownership during refactors.
"""

from __future__ import annotations

import argparse
import fnmatch
import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


DEFAULT_THRESHOLD = 500
DEFAULT_EXTS = {".go", ".svelte", ".ts"}
DEFAULT_EXCLUDES = {
    ".git",
    "web/dist",
    "web/node_modules",
    "vendor",
    ".omp",
}
# Known large/generated/data-heavy files. Exempt from "needs split" pressure,
# not from visibility. Keep reasons factual; confirm ownership before editing.
DEFAULT_EXEMPTIONS = {
    "web/src/lib/models.ts": "auto-generated catalog data",
    "internal/providers/provider_registry.go": "generated provider metadata (DO NOT EDIT)",
    "internal/providers/registry_models.go": "large static provider model catalog/data",
    "tools/sitegen/wiki_template.go": "template/data blob",
}


@dataclass(frozen=True)
class Entry:
    lines: int
    path: str
    kind: str
    exempt_reason: str | None


def git_root() -> Path:
    try:
        out = subprocess.check_output(
            ["git", "rev-parse", "--show-toplevel"], stderr=subprocess.DEVNULL, text=True
        ).strip()
        if out:
            return Path(out)
    except Exception:
        pass
    return Path.cwd()


def rel(path: Path, root: Path) -> str:
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except Exception:
        return path.as_posix()


def is_excluded(rel_path: str, excludes: set[str]) -> bool:
    rp = rel_path.lstrip("./")
    for ex in excludes:
        ex = ex.strip("/").lstrip("./")
        if not ex:
            continue
        if rp == ex or rp.startswith(ex + "/"):
            return True
    return False


def kind_for(path: str) -> str:
    if path.endswith("_test.go"):
        return "go_test"
    if path.endswith(".go"):
        return "go_prod"
    if path.endswith(".svelte"):
        return "svelte"
    if path.endswith(".ts"):
        return "ts"
    return "other"


def load_exemptions(path: Path | None) -> dict[str, str]:
    exemptions = dict(DEFAULT_EXEMPTIONS)
    if path is None or not path.exists():
        return exemptions
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "\t" in line:
            pat, reason = line.split("\t", 1)
        elif "  " in line:
            pat, reason = line.split("  ", 1)
        else:
            pat, reason = line, "exempt"
        exemptions[pat.strip()] = reason.strip() or "exempt"
    return exemptions


def exempt_reason(path: str, exemptions: dict[str, str]) -> str | None:
    for pat, reason in exemptions.items():
        if fnmatch.fnmatch(path, pat) or path == pat:
            return reason
    return None


def iter_files(root: Path, exts: set[str], excludes: set[str]):
    for dirpath, dirnames, filenames in os.walk(root):
        dirpath_p = Path(dirpath)
        rel_dir = rel(dirpath_p, root)
        # prune excluded dirs in-place
        kept = []
        for d in dirnames:
            rp = rel(dirpath_p / d, root)
            if not is_excluded(rp, excludes):
                kept.append(d)
        dirnames[:] = kept
        for name in filenames:
            p = dirpath_p / name
            rp = rel(p, root)
            if is_excluded(rp, excludes):
                continue
            if p.suffix not in exts:
                continue
            yield rp, p


def count_lines(path: Path) -> int:
    # Count physical lines; inexpensive and stable for audit/reporting.
    with path.open("rb") as f:
        return sum(1 for _ in f)


def collect(root: Path, threshold: int, exts: set[str], excludes: set[str], exemptions: dict[str, str]) -> list[Entry]:
    entries: list[Entry] = []
    for rp, p in iter_files(root, exts, excludes):
        n = count_lines(p)
        if n <= threshold:
            continue
        entries.append(Entry(n, rp, kind_for(rp), exempt_reason(rp, exemptions)))
    entries.sort(key=lambda e: (-e.lines, e.path))
    return entries


def print_summary(entries: list[Entry], threshold: int) -> None:
    by_kind: dict[str, list[Entry]] = {}
    for e in entries:
        by_kind.setdefault(e.kind, []).append(e)
    print(f"Files > {threshold} LOC (review signal, not a forced cut target)")
    print()
    print("| Kind | Files | Total lines | Exempt |")
    print("|---|---:|---:|---:|")
    total = 0
    for kind in ["go_prod", "go_test", "svelte", "ts", "other"]:
        group = by_kind.get(kind, [])
        if not group:
            continue
        lines = sum(e.lines for e in group)
        total += lines
        exempt = sum(1 for e in group if e.exempt_reason)
        print(f"| {kind} | {len(group)} | {lines} | {exempt} |")
    exempt_total = sum(1 for e in entries if e.exempt_reason)
    print(f"| **total** | **{len(entries)}** | **{sum(e.lines for e in entries)}** | **{exempt_total}** |")
    print()
    buckets = [(">=2000", 2000, 10**12), ("1000-1999", 1000, 1999), ("700-999", 700, 999), (f"{threshold+1}-699" if threshold < 699 else f"{threshold+1}+", threshold + 1, 699 if threshold < 699 else 10**12)]
    # normalize buckets around common ranges, but keep threshold meaningful
    norm = []
    if threshold >= 700:
        norm = [(f">={threshold}", threshold + 1, 10**12)]
    else:
        norm = [(">=2000", 2000, 10**12), ("1000-1999", 1000, 1999), ("700-999", 700, 999), (f"{threshold+1}-699", threshold + 1, 699)]
    print("Production buckets (go_prod/svelte/ts):")
    print()
    print("| Bucket | Files | Lines |")
    print("|---|---:|---:|")
    prod = [e for e in entries if e.kind in {"go_prod", "svelte", "ts"}]
    for label, lo, hi in norm:
        group = [e for e in prod if lo <= e.lines <= hi]
        if not group and label != norm[-1][0]:
            # still show empty common buckets? no, keep concise
            pass
        print(f"| {label} | {len(group)} | {sum(e.lines for e in group)} |")
    print()


def print_table(entries: list[Entry]) -> None:
    print("| Lines | Kind | Exempt | File | Reason |")
    print("|---:|---|:---:|---|---|")
    for e in entries:
        exempt = "yes" if e.exempt_reason else ""
        reason = e.exempt_reason or ""
        print(f"| {e.lines} | {e.kind} | {exempt} | `{e.path}` | {reason} |")


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--threshold", type=int, default=DEFAULT_THRESHOLD, help=f"line threshold (default: {DEFAULT_THRESHOLD})")
    p.add_argument("--root", default=None, help="repo/root to scan (default: git root)")
    p.add_argument("--exemptions", default=None, help="optional exemptions file: '<glob-or-path> <tab/spaces> reason'")
    p.add_argument("--format", choices=["text", "tsv"], default="text", help="output format")
    p.add_argument("--no-summary", action="store_true", help="omit summary")
    p.add_argument("--kind", action="append", default=[], help="filter kind: go_prod,go_test,svelte,ts,other (repeatable/comma-separated)")
    return p.parse_args()


def main() -> int:
    args = parse_args()
    root = Path(args.root).resolve() if args.root else git_root().resolve()
    exemptions = load_exemptions(Path(args.exemptions).resolve() if args.exemptions else None)
    entries = collect(root, args.threshold, set(DEFAULT_EXTS), set(DEFAULT_EXCLUDES), exemptions)
    if args.kind:
        wanted: set[str] = set()
        for item in args.kind:
            wanted.update(x.strip() for x in item.split(",") if x.strip())
        entries = [e for e in entries if e.kind in wanted]

    if args.format == "tsv":
        if not args.no_summary:
            print("lines\tkind\texempt\tpath\treason")
        for e in entries:
            print(f"{e.lines}\t{e.kind}\t{e.exempt_reason or ''}\t{e.path}\t{e.exempt_reason or ''}")
        return 0

    print("# File-size audit")
    print()
    print(f"- Root: `{root}`")
    print(f"- Threshold: `>{args.threshold}` LOC")
    print("- Policy: review signal only; **do not force-cut** cohesive files. Split only on clear context/domain boundaries; preserve behavior/contracts/domain ownership.")
    print("- Exemptions: generated/data-heavy or explicitly exempt files are visible but not split-pressure by default.")
    print()
    if not args.no_summary:
        print_summary(entries, args.threshold)
    print_table(entries)
    print()
    print("_Non-blocking report. Use this to find mixed-responsibility hotspots, not to force mechanical 500-line splits._")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
