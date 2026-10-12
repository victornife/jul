# Publishing the Jul.IA website

The public website renders the repository's canonical Markdown. It has no
separate copy of the vision, no client JavaScript, and no external fonts or
analytics. The gateway's build and runtime dependencies are unchanged.

| Website page | Canonical source |
| --- | --- |
| Homepage | [Brief vision](../docs/vision/brief.md) |
| Our vision | [Longer vision and mission](../docs/vision/README.md) |
| Practical guide | [Practical guide](../docs/practical-guide.md) |
| Get started | [Getting started](../docs/getting-started.md) |
| Documentation | [Documentation index](../docs/index.md) |

Links between these pages stay within the website. Other repository references
lead to their canonical GitHub files or directories. Relative site URLs support
both a project subpath and a custom domain. The existing logo is copied from
[docs/assets/logo.png](../docs/assets/logo.png).

## Build and preview locally

Use Python 3.11 or later:

```bash
python3 -m pip install --require-hashes -r website/requirements.txt
python3 scripts/test_build_site.py
python3 scripts/build-site.py
python3 -m http.server 8000 --directory dist/site
```

Open `http://127.0.0.1:8000/`. On Windows, use `python` if that is the installed
Python command. Generated output is under the ignored `dist/site` directory;
edit the Markdown, template or stylesheet, then rebuild.

The build checks rendered internal links and anchors. Missing source targets,
missing assets, broken anchors and unsafe link schemes fail publication. The
tests cover source updates, navigation, tables, literal command examples and
failure boundaries.

## GitHub Pages

The [website workflow](../.github/workflows/website.yml) builds and checks pull
requests, retains the generated preview artifact, and deploys pushes to `main`
after GitHub Pages has been enabled. Deployment uses the `github-pages`
environment and the official Pages actions, with deployment permissions kept
out of pull-request builds.

Repository administrators must first select **Settings → Pages → Build and
deployment → Source → GitHub Actions**. This is an administration setting;
the workflow's `GITHUB_TOKEN` cannot enable Pages on a disabled repository.
After activation, rerun the website workflow or dispatch it from `main`.

The initial project URL is `https://victornife.github.io/jul/`. A custom domain
can be configured later without changing authored content or relative links.
Changes to the website, canonical Markdown, logo or build scripts trigger a new
build. The workflow reports a skipped deployment explicitly while Pages is
disabled; a successful build is not reported as a live website.

Keep the public statements aligned with the available gateway and its
documented boundaries. The practical guide references a release and must be
reviewed when that release, capabilities or deployment evidence change.
