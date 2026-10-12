#!/usr/bin/env python3
"""Render the public site from the repository's canonical Markdown pages."""

from __future__ import annotations

import argparse
import html
import json
import posixpath
import shutil
from dataclasses import dataclass
from html.parser import HTMLParser
from pathlib import Path
from string import Template
from urllib.parse import quote, unquote, urlsplit, urlunsplit
from xml.etree.ElementTree import Element

import markdown
from markdown.extensions import Extension
from markdown.treeprocessors import Treeprocessor

ROOT = Path(__file__).resolve().parent.parent
REPOSITORY = "https://github.com/victornife/jul"


@dataclass(frozen=True)
class Page:
    source: str
    output: str
    label: str


PAGES = (
    Page("docs/vision/brief.md", "index.html", "Open-source application gateway"),
    Page("docs/vision/README.md", "vision/index.html", "Our vision"),
    Page("docs/practical-guide.md", "guide/index.html", "Practical guide"),
    Page("docs/getting-started.md", "get-started/index.html", "Get started"),
    Page("docs/index.md", "docs/index.html", "Documentation"),
)
OUTPUTS = {page.source: page.output for page in PAGES}


def relative_url(output: str, target: str) -> str:
    """Relative URLs work at /, /jul/, and a custom domain without rewriting."""
    return posixpath.relpath(target, posixpath.dirname(output) or ".")


def resolve_link(root: Path, page: Page, url: str) -> str:
    parsed = urlsplit(url)
    if parsed.scheme:
        if parsed.scheme.lower() not in {"https", "http", "mailto"}:
            raise ValueError(f"{page.source}: unsupported URL scheme: {url}")
        return url
    if parsed.netloc:
        raise ValueError(f"{page.source}: protocol-relative URL: {url}")
    if not parsed.path:
        return url

    target = (root / page.source).parent / unquote(parsed.path)
    target = target.resolve()
    try:
        source = target.relative_to(root.resolve()).as_posix()
    except ValueError as exc:
        raise ValueError(f"{page.source}: link leaves the repository: {url}") from exc
    if not target.exists():
        raise ValueError(f"{page.source}: missing link target: {url}")
    if target.is_dir():
        index = target / "README.md"
        if index.is_file():
            source = index.relative_to(root.resolve()).as_posix()

    if source in OUTPUTS:
        path = relative_url(page.output, OUTPUTS[source])
    else:
        kind = "tree" if target.is_dir() else "blob"
        path = f"{REPOSITORY}/{kind}/main/{quote(source, safe='/')}"
    return urlunsplit(("", "", path, parsed.query, parsed.fragment))


class RepositoryLinks(Treeprocessor):
    def __init__(self, md, root: Path, page: Page):
        super().__init__(md)
        self.root = root
        self.page = page
        self.heading = ""

    def run(self, element):
        heading = element.find("h1")
        if heading is None:
            raise ValueError(f"{self.page.source}: expected a document title")
        self.heading = "".join(heading.itertext())
        element.remove(heading)
        for node in list(element.iter()):
            for attribute in ("href", "src"):
                value = node.get(attribute)
                if value:
                    node.set(attribute, resolve_link(self.root, self.page, value))
            # Preserve the native table while giving narrow screens a scroll area.
            for index, child in list(enumerate(node)):
                if child.tag == "table":
                    wrapper = Element("div", {"class": "table-scroll", "tabindex": "0",
                                              "role": "region", "aria-label": "Scrollable table"})
                    node.remove(child)
                    wrapper.append(child)
                    node.insert(index, wrapper)


class LinkExtension(Extension):
    def __init__(self, root: Path, page: Page):
        super().__init__()
        self.root, self.page = root, page
        self.processor = None

    def extendMarkdown(self, md):
        self.processor = RepositoryLinks(md, self.root, self.page)
        # Remove the source H1 before TOC generates anchors; the template owns H1.
        md.treeprocessors.register(self.processor, "repository-links", 6)


class Text(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []

    def handle_data(self, data):
        self.parts.append(data)


class OutputLinks(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = set()
        self.links = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if values.get("id"):
            if values["id"] in self.ids:
                raise ValueError(f"duplicate HTML id: {values['id']}")
            self.ids.add(values["id"])
        for key in ("href", "src"):
            if values.get(key):
                self.links.append(values[key])


def validate_output(output: Path) -> None:
    """Check actual rendered links, including cross-page and local anchors."""
    documents = {}
    for file in sorted(output.rglob("*.html")):
        parser = OutputLinks()
        parser.feed(file.read_text(encoding="utf-8"))
        documents[file.resolve()] = parser
    for file, document in documents.items():
        for url in document.links:
            parsed = urlsplit(url)
            if parsed.scheme:
                if parsed.scheme.lower() not in {"https", "http", "mailto"}:
                    raise ValueError(f"{file.name}: unsupported output URL scheme: {url}")
                continue
            if parsed.netloc:
                raise ValueError(f"{file.name}: protocol-relative output URL: {url}")
            target = (file.parent / unquote(parsed.path)).resolve() if parsed.path else file
            if target.is_dir():
                target /= "index.html"
            if not target.is_relative_to(output.resolve()) or not target.is_file():
                raise ValueError(f"{file.name}: broken output link: {url}")
            if parsed.fragment and target.suffix == ".html":
                if unquote(parsed.fragment) not in documents[target].ids:
                    raise ValueError(f"{file.name}: broken output anchor: {url}")


def render_page(root: Path, page: Page, template: Template) -> str:
    extension = LinkExtension(root, page)
    renderer = markdown.Markdown(extensions=["fenced_code", "tables", "toc", extension],
                                 output_format="html")
    body = renderer.convert((root / page.source).read_text(encoding="utf-8"))
    heading = extension.processor.heading
    if page.output == "index.html":
        heading = heading.removeprefix("Jul.IA — ")

    nav = []
    for label, target in (("Our vision", "vision/index.html"),
                          ("Practical guide", "guide/index.html"),
                          ("Documentation", "docs/index.html"),
                          ("Get started", "get-started/index.html")):
        current = ' aria-current="page"' if page.output == target else ""
        css = ' class="nav-start"' if label == "Get started" else ""
        nav.append(f'<a href="{relative_url(page.output, target)}"{css}{current}>{label}</a>')

    home = page.output == "index.html"
    hero_extra = ""
    if home:
        hero_extra = ('<div class="hero-art"><img src="assets/logo.png" '
                      'alt="Jul.IA gateway illustration" width="1024" height="1024"></div>')
    actions = ""
    if home:
        actions = ('<div class="hero-actions">'
                   f'<a class="button" href="{relative_url(page.output, "get-started/index.html")}">Get started</a>'
                   f'<a class="button secondary" href="{relative_url(page.output, "vision/index.html")}">Explore our vision</a>'
                   '</div>')
    parser = Text()
    parser.feed(body)
    description = " ".join(" ".join(parser.parts).split())[:180]
    return template.substitute(
        document_title=html.escape("Jul.IA — " + page.label),
        description=html.escape(description, quote=True),
        page_class="home" if home else "reading",
        home_url=relative_url(page.output, "index.html"),
        stylesheet_url=relative_url(page.output, "assets/site.css"),
        icon_url=relative_url(page.output, "assets/logo.png"),
        label=html.escape(page.label),
        heading=html.escape(heading),
        nav="".join(nav),
        hero_extra=hero_extra,
        actions=actions,
        body=body,
        toc=renderer.toc,
        repository=REPOSITORY,
        source_url=f"{REPOSITORY}/blob/main/{page.source}",
    )


def build(root: Path | None = None) -> Path:
    root = root or ROOT
    output = root / "dist" / "site"
    output.mkdir(parents=True, exist_ok=True)
    template = Template((root / "website" / "page.html").read_text(encoding="utf-8"))
    for page in PAGES:
        destination = output / page.output
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(render_page(root, page, template), encoding="utf-8")
    assets = output / "assets"
    assets.mkdir(exist_ok=True)
    shutil.copyfile(root / "website" / "site.css", assets / "site.css")
    shutil.copyfile(root / "docs" / "assets" / "logo.png", assets / "logo.png")
    (output / ".nojekyll").write_text("", encoding="utf-8")
    (output / "sources.json").write_text(json.dumps(OUTPUTS, indent=2) + "\n", encoding="utf-8")
    validate_output(output)
    return output


def main() -> int:
    argparse.ArgumentParser(description=__doc__).parse_args()
    try:
        output = build()
    except (OSError, ValueError) as exc:
        print(f"Site build failed: {exc}")
        return 1
    print(f"Built and validated {len(PAGES)} canonical pages in {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
