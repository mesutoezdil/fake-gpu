#!/usr/bin/env python3
"""Check relative links and anchors in every tracked Markdown file.

    scripts/check-links.py

Links are checked the way GitHub renders them: relative to the file, with
anchors matched against GitHub's heading slugs. External URLs are skipped.
"""
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
LINK = re.compile(r'(?<!\\)!?\[[^\]]*\]\(([^\s)]+)(?:\s+"[^"]*")?\)')
HEADING = re.compile(r'^(#{1,6})\s+(.*?)\s*#*\s*$')
FENCE = re.compile(r'^\s*(```|~~~)')
SKIP = ('charts/fake-gpu/charts/',)


def slug(heading):
    text = re.sub(r'`([^`]*)`', r'\1', heading)
    text = re.sub(r'\[([^\]]*)\]\([^)]*\)', r'\1', text)
    text = re.sub(r'[^\w\- ]', '', text.lower())
    return text.replace(' ', '-')


def body_lines(path):
    """Lines outside fenced code blocks."""
    fenced = False
    for number, line in enumerate(path.read_text().splitlines(), 1):
        if FENCE.match(line):
            fenced = not fenced
            continue
        if not fenced:
            yield number, line


def anchors(path, cache={}):  # noqa: B006  (memo shared across calls)
    if path not in cache:
        counts, found = {}, set()
        for _, line in body_lines(path):
            match = HEADING.match(line)
            if match:
                base = slug(match[2])
                # GitHub numbers repeated headings: name, name-1, name-2.
                found.add(f'{base}-{counts[base]}' if base in counts else base)
                counts[base] = counts.get(base, 0) + 1
        cache[path] = found
    return cache[path]


def main():
    files = subprocess.run(['git', 'ls-files', '-co', '--exclude-standard', '*.md'], cwd=ROOT,
                           capture_output=True, text=True, check=True).stdout.split()
    errors = []
    for name in files:
        if name.startswith(SKIP) or name.startswith('tmp/'):
            continue
        path = ROOT / name
        for number, line in body_lines(path):
            for target in LINK.findall(re.sub(r'`[^`]*`', '', line)):
                parsed = urlsplit(target)
                if parsed.scheme or parsed.netloc or target.startswith('mailto:'):
                    continue
                destination = (path.parent / unquote(parsed.path)).resolve() if parsed.path else path
                where = f'{name}:{number}: {target}'
                if not destination.exists():
                    errors.append(where + ' (missing)')
                elif parsed.fragment and destination.suffix == '.md' and parsed.fragment not in anchors(destination):
                    errors.append(where + ' (no such heading)')
    for error in errors:
        print(error)
    print(f'{len(files)} Markdown files, {len(errors)} broken links', file=sys.stderr)
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
