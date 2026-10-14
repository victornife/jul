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
from xml.etree.ElementTree import Element, SubElement

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
    Page("docs/examples/README.md", "examples/index.html", "Examples"),
    Page("docs/practical-guide.md", "guide/index.html", "Practical guide"),
    Page("docs/getting-started.md", "get-started/index.html", "Get started"),
    Page("docs/index.md", "docs/index.html", "Documentation"),
)
OUTPUTS = {page.source: page.output for page in PAGES}
EXAMPLES_SOURCE = "docs/examples/README.md"
IMAGE_SUFFIXES = {".png", ".svg", ".webp"}


def asset_output(source: str) -> str | None:
    """Publish the example images locally rather than linking to a blob page."""
    if source.startswith("docs/examples/assets/") and Path(source).suffix in IMAGE_SUFFIXES:
        return "assets/examples/" + source.removeprefix("docs/examples/assets/")
    return None


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

    asset = asset_output(source)
    if asset:
        path = quote(relative_url(page.output, asset), safe="/")
    elif source in OUTPUTS:
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


class ExamplesLayout(Treeprocessor):
    """Present canonical Markdown as cards and figures without duplicating prose."""

    def __init__(self, md, links: RepositoryLinks):
        super().__init__(md)
        self.links = links

    @staticmethod
    def groups(nodes):
        """Keep every node while grouping H3 sections for the visual cards."""
        intro, groups, current = [], [], None
        for node in nodes:
            if node.tag == "h3":
                current = [node]
                groups.append(current)
            elif current is None:
                intro.append(node)
            else:
                current.append(node)
        return intro, groups

    def run(self, element):
        for table in element.iter("table"):
            labels = ["".join(th.itertext()) for th in table.findall("./thead/tr/th")]
            for row in table.findall("./tbody/tr"):
                for cell, label in zip(row, labels):
                    cell.set("data-label", label)

        # A picture keeps one alt text while selecting the readable phone diagram.
        for parent in list(element.iter()):
            for index, node in list(enumerate(parent)):
                if node.tag != "img" or not node.get("src", "").endswith(
                        ("/sharing-map.svg", "/sensor-flow.svg")):
                    continue
                source = node.get("src")
                picture = Element("picture", {"class": "diagram-picture"})
                SubElement(picture, "source", {"media": "(max-width: 680px)",
                           "srcset": source.removesuffix(".svg") + "-mobile.svg"})
                parent.remove(node)
                picture.append(node)
                parent.insert(index, picture)

        intro, sections, current = [], [], None
        for node in list(element):
            if node.tag == "h2":
                current = [node]
                sections.append(current)
            elif current is None:
                intro.append(node)
            else:
                current.append(node)
        element[:] = []
        hero = SubElement(element, "section", {"class": "hero", "aria-labelledby": "page-title"})
        copy = SubElement(hero, "div", {"class": "hero-text"})
        SubElement(copy, "p", {"class": "eyebrow"}).text = "Examples · Jul.IA"
        heading = SubElement(copy, "h1", {"id": "page-title"})
        title = self.links.heading.removeprefix("Examples — ")
        first, separator, rest = title.partition(". ")
        heading.text = first + "." if separator else title
        if separator:
            SubElement(heading, "br")
            SubElement(heading, "span").text = rest
        image_index = next((index for index, node in enumerate(intro)
                            if node.tag == "p" and node.find("img") is not None), len(intro))
        copy.extend(intro[:image_index])
        if image_index < len(intro):
            art = SubElement(hero, "div", {"class": "hero-art"})
            art.extend(intro[image_index:])
        scenario_id = next((nodes[0].get("id") for nodes in sections
                            if "".join(nodes[0].itertext()) == "Five things you could do"), None)
        if scenario_id:
            action = SubElement(copy, "a", {"class": "button", "href": "#" + scenario_id})
            action.text = "Find your first project "
            SubElement(action, "span", {"aria-hidden": "true"}).text = "↓"

        for nodes in sections:
            title, *content = nodes
            identifier = title.attrib.pop("id")
            title.set("id", identifier + "-title")
            section = SubElement(element, "section", {"id": identifier,
                                 "class": "page-section section-" + identifier,
                                 "aria-labelledby": title.get("id")})
            head = SubElement(section, "div", {"class": "section-heading"})
            head.append(title)
            if identifier == "five-things-you-could-do":
                before, cards = self.groups(content)
                section.extend(before)
                grid = SubElement(section, "div", {"class": "scenario-grid"})
                for number, card in enumerate(cards, 1):
                    article = SubElement(grid, "article", {"class": f"scenario scenario-{number}"})
                    SubElement(article, "span", {"class": "scenario-number"}).text = f"{number:02}"
                    article.extend(card)
            elif identifier == "more-possibilities":
                before, cards = self.groups(content)
                section.extend(before)
                after = []
                if cards and cards[-1][-1].tag == "p" and "".join(
                        cards[-1][-1].itertext()).startswith("These are possibilities"):
                    after.append(cards[-1].pop())
                grid = SubElement(section, "div", {"class": "possibility-grid"})
                for card in cards:
                    SubElement(grid, "article").extend(card)
                section.extend(after)
            else:
                section.extend(content)


class LinkExtension(Extension):
    def __init__(self, root: Path, page: Page):
        super().__init__()
        self.root, self.page = root, page
        self.processor = None

    def extendMarkdown(self, md):
        self.processor = RepositoryLinks(md, self.root, self.page)
        # Remove the source H1 before TOC generates anchors; the template owns H1.
        md.treeprocessors.register(self.processor, "repository-links", 6)
        if self.page.source == EXAMPLES_SOURCE:
            md.treeprocessors.register(ExamplesLayout(md, self.processor), "examples-layout", 4)


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
        if values.get("srcset"):
            for item in values["srcset"].split(","):
                fields = item.strip().split()
                if not fields:
                    raise ValueError("empty responsive image candidate")
                self.links.append(fields[0])


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
    examples = page.source == EXAMPLES_SOURCE
    if examples:
        template = Template((root / "website" / "examples.html").read_text(encoding="utf-8"))
    extension = LinkExtension(root, page)
    renderer = markdown.Markdown(extensions=["fenced_code", "tables", "toc", extension],
                                 output_format="html")
    body = renderer.convert((root / page.source).read_text(encoding="utf-8"))
    heading = extension.processor.heading
    if page.output == "index.html":
        heading = heading.removeprefix("Jul.IA — ")

    nav = []
    for label, target in (("Our vision", "vision/index.html"),
                          ("Examples", "examples/index.html"),
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
        page_class="examples" if examples else "home" if home else "reading",
        home_url=relative_url(page.output, "index.html"),
        stylesheet_url=relative_url(page.output, "assets/site.css"),
        examples_stylesheet_url=relative_url(page.output, "assets/examples.css"),
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
    shutil.copyfile(root / "website" / "examples.css", assets / "examples.css")
    shutil.copyfile(root / "docs" / "assets" / "logo.png", assets / "logo.png")
    for source in sorted((root / "docs" / "examples" / "assets").rglob("*")):
        if source.is_file():
            target = asset_output(source.relative_to(root).as_posix())
            if target:
                if not source.resolve().is_relative_to(root.resolve()):
                    raise ValueError(f"{source}: asset leaves the repository")
                destination = output / target
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, destination)
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
