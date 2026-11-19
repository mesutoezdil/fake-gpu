# Build and publish the documentation

The site uses Material for MkDocs and the existing repository Markdown. Demo
READMEs remain the source for runnable walkthroughs. The staging
script preserves Markdown pages and images and points other source-file links
back to GitHub. It writes only under `tmp/`.

```bash
python3 -m venv tmp/docs-venv
. tmp/docs-venv/bin/activate
python -m pip install -r scripts/docs/requirements.txt
python scripts/docs/build.py
# Preview at http://127.0.0.1:8001; stop with Ctrl-C.
python scripts/docs/build.py --serve
```

Navigation is in `mkdocs.yml`. `docs/index.md` becomes the site's home page,
and the quick-start page is extracted from the root README during staging, so
installation commands have one source. Add new demos to the navigation and the
demo catalog; distinguish implemented behavior from plans.

Pages live under `docs/` by kind: task guides in `docs/guides/` (all with the
same sections: Overview, Requirements, Steps, Verify, How it works,
Troubleshooting, Clean up, Related), reference pages in `docs/reference/` and
contributor pages in `docs/contributing/`.

Check every relative link and heading anchor in the repository's Markdown, the
way GitHub renders them. The `lint` workflow runs this on every pull request:

```bash
scripts/check-links.py
```

The `docs-site` GitHub Actions workflow validates builds on pull requests.
On a push to `main`, or manual dispatch, it uploads the built site and deploys
through GitHub Pages. Repository Settings → Pages → Source must be **GitHub
Actions**. Once deployed, the intended address is
[the project documentation](https://mesutoezdil.github.io/fake-gpu/).
Local changes alone do not publish the site.
