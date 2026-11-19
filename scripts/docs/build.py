#!/usr/bin/env python3
"""Stage repository documentation without modifying its Markdown sources."""
import argparse
import os
import re
import shutil
import subprocess
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
STAGE = ROOT / 'tmp/docs-source'
SOURCE_URL = 'https://github.com/mesutoezdil/fake-gpu/blob/main/'
LINK = re.compile(r'(!?\[[^\]]*\]\()([^\s)]+)([^)]*\))')


# GitHub's index of docs/; the site has its own home page and navigation.
EXCLUDED = {ROOT / 'docs/README.md'}


def staged(path):
    """Where a repository file lands in the staged site."""
    if path == ROOT / 'README.md':
        return STAGE / 'repository.md'
    if path == ROOT / 'docs/index.md':
        return STAGE / 'index.md'
    return STAGE / path.relative_to(ROOT)


def relink(text, source_dir, destination, staged_files):
    """Rewrite relative links in text written at source_dir for destination.

    Pages and images that are part of the site stay relative links; links to
    other repository files point at GitHub, where they can be read.
    """
    def rewrite(match):
        target = match[2]
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or target.startswith('#'):
            return match[0]
        resolved = (source_dir / unquote(parsed.path)).resolve()
        fragment = '#' + parsed.fragment if parsed.fragment else ''
        if resolved in staged_files:
            relative = os.path.relpath(staged(resolved), destination.parent)
            return match[1] + Path(relative).as_posix() + fragment + match[3]
        try:
            relative = resolved.relative_to(ROOT).as_posix()
        except ValueError:
            return match[0]
        return match[1] + SOURCE_URL + relative + fragment + match[3]
    return LINK.sub(rewrite, text)


def stage():
    if STAGE.exists():
        shutil.rmtree(STAGE)
    STAGE.mkdir(parents=True)
    sources = [ROOT / 'README.md']
    for directory in ('docs', 'demo'):
        sources.extend(p for p in (ROOT / directory).rglob('*') if p.is_file() and '__pycache__' not in p.parts
                       and not p.name.startswith('.') and p not in EXCLUDED)
    staged_files = {p.resolve() for p in sources if p.suffix in ('.md', '.png', '.jpg', '.svg', '.gif')}
    for source in sources:
        destination = staged(source)
        destination.parent.mkdir(parents=True, exist_ok=True)
        if source.suffix == '.md':
            destination.write_text(relink(source.read_text(), source.parent, destination, staged_files))
        else:
            shutil.copyfile(source, destination)
    # The quick start has one source: the README section.
    readme = (ROOT / 'README.md').read_text()
    quickstart = readme.split('## On Kubernetes\n', 1)[1].split('\n## ', 1)[0] + '\n'
    page = STAGE / 'docs/getting-started.md'
    page.write_text('# Quick start\n\n' + relink(quickstart, ROOT, page, staged_files)
                    + '\nSee the [telemetry guide](guides/telemetry.md) for Grafana and dashboard details.\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--serve', action='store_true', help='Preview at http://127.0.0.1:8001')
    args = parser.parse_args()
    stage()
    command = ['mkdocs', 'serve', '--dev-addr', '127.0.0.1:8001'] if args.serve else ['mkdocs', 'build', '--strict']
    subprocess.run(command, cwd=ROOT, check=True)


if __name__ == '__main__':
    main()
