#!/usr/bin/env python3
"""Build the documentation site from the repository's Markdown.

    scripts/docs/build.py            # build into tmp/docs-site, strictly
    scripts/docs/build.py --serve    # preview at http://127.0.0.1:8001

Sources are never modified. They are staged under tmp/docs-source: pages and
images keep relative links between each other, links to any other repository
file point at it on GitHub, the README becomes the "repository" page, and its
"On Kubernetes" section becomes the quick start, so install commands have
one source.
"""
import argparse
import os
import re
import shutil
import subprocess
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
STAGE = ROOT / 'tmp/docs-source'
GITHUB = 'https://github.com/mesutoezdil/fake-gpu/blob/main/'
LINK = re.compile(r'(!?\[[^\]]*\]\()([^\s)]+)([^)]*\))')
PAGE_SUFFIXES = {'.md', '.png', '.jpg', '.svg', '.gif'}
# docs/README.md is GitHub's index of docs/; the site has its own home page.
SKIP = {ROOT / 'docs/README.md'}
# Where a file lands in the site when not at its repository path.
PLACED = {ROOT / 'README.md': 'repository.md', ROOT / 'docs/index.md': 'index.md'}


def sources():
    """The README plus every page and image under docs/ and demo/."""
    found = [ROOT / 'README.md']
    for top in ('docs', 'demo'):
        found += sorted(p for p in (ROOT / top).rglob('*')
                        if p.is_file() and p.suffix in PAGE_SUFFIXES and p not in SKIP and not p.name.startswith('.'))
    return found


def site_path(path):
    return STAGE / PLACED.get(path, path.relative_to(ROOT))


class Linker:
    """Rewrites relative links for a page's place in the site."""

    def __init__(self, pages):
        self.pages = {p.resolve() for p in pages}

    def __call__(self, text, written_in, lands_at):
        def rewrite(m):
            target = urlsplit(m[2])
            if target.scheme or target.netloc or m[2].startswith('#'):
                return m[0]
            resolved = (written_in / unquote(target.path)).resolve()
            anchor = '#' + target.fragment if target.fragment else ''
            if resolved in self.pages:
                new = Path(os.path.relpath(site_path(resolved), lands_at.parent)).as_posix()
            else:
                try:
                    new = GITHUB + resolved.relative_to(ROOT).as_posix()
                except ValueError:
                    return m[0]  # outside the repository: leave it
            return m[1] + new + anchor + m[3]
        return LINK.sub(rewrite, text)


def stage():
    shutil.rmtree(STAGE, ignore_errors=True)
    pages = sources()
    link = Linker(pages)
    for src in pages:
        dst = site_path(src)
        dst.parent.mkdir(parents=True, exist_ok=True)
        if src.suffix == '.md':
            dst.write_text(link(src.read_text(), src.parent, dst))
        else:
            shutil.copyfile(src, dst)
    readme = (ROOT / 'README.md').read_text()
    section = readme.split('## On Kubernetes\n', 1)[1].split('\n## ', 1)[0]
    quickstart = STAGE / 'docs/getting-started.md'
    quickstart.write_text('# Quick start\n\n' + link(section + '\n', ROOT, quickstart)
                          + '\nSee the [telemetry guide](guides/telemetry.md) for Grafana and dashboard details.\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--serve', action='store_true', help='preview at http://127.0.0.1:8001')
    serve = parser.parse_args().serve
    stage()
    command = ['mkdocs', 'serve', '--dev-addr', '127.0.0.1:8001'] if serve else ['mkdocs', 'build', '--strict']
    subprocess.run(command, cwd=ROOT, check=True)


if __name__ == '__main__':
    main()
