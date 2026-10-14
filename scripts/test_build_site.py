#!/usr/bin/env python3
"""Exercise publication, Markdown semantics, and broken-link failure boundaries."""

import contextlib
import importlib.util
import io
import json
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("build_site", Path(__file__).with_name("build-site.py"))
site = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = site
SPEC.loader.exec_module(site)


class SiteTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        for page in site.PAGES:
            path = self.root / page.source
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("# Jul.IA — Title\n\n## A section\n\nCanonical prose.\n", encoding="utf-8")
        assets = self.root / "docs" / "assets"
        assets.mkdir(exist_ok=True)
        (assets / "logo.png").write_bytes(b"logo fixture")
        shutil.copytree(site.ROOT / "website", self.root / "website")
        self.brief = self.root / site.PAGES[0].source
        self.long = self.root / site.PAGES[1].source

    def test_full_build_renders_sources_and_rebuilds_after_an_edit(self):
        self.brief.write_text("# Jul.IA — Approved title\n\n## Our mission\n\nApproved prose — café.\n", encoding="utf-8")
        output = site.build(self.root)
        html = (output / "index.html").read_text()
        self.assertIn("Approved prose — café.", html)
        self.assertIn('<h1 id="page-title">Approved title</h1>', html)
        self.assertEqual(html.count("<h1"), 1)
        self.assertEqual(json.loads((output / "sources.json").read_text())["docs/vision/brief.md"], "index.html")
        self.assertIn('href="../index.html"', (output / "vision/index.html").read_text())
        self.brief.write_text(self.brief.read_text().replace("Approved prose", "Revised prose"))
        site.build(self.root)
        self.assertIn("Revised prose", (output / "index.html").read_text())
        self.assertEqual((output / "assets/logo.png").read_bytes(), b"logo fixture")

    def test_tables_fenced_commands_and_toc_are_preserved(self):
        self.brief.write_text('''# Jul.IA — Commands

## Local preview

| Capability | State |
| --- | --- |
| TLS | Configurable |

```bash
printf '<h1>Hello</h1>\\n'
./jul run --listen 127.0.0.1:8080
```
''')
        output = site.build(self.root)
        html = (output / "index.html").read_text()
        self.assertIn('class="table-scroll"', html)
        self.assertIn("<table>", html)
        self.assertIn("&lt;h1&gt;Hello&lt;/h1&gt;", html)
        self.assertIn("127.0.0.1:8080", html)
        self.assertIn('href="#local-preview"', html)
        self.assertNotIn('href="#julia-commands"', html)

    def test_published_pages_stay_local_and_technical_references_go_to_github(self):
        (self.root / "docs/status.md").write_text("# Status\n")
        (self.root / "docs/adr").mkdir()
        self.long.write_text('''# Jul.IA — Vision

## A section

[Brief](brief.md#a-section) · [Guide](../practical-guide.md) ·
[Status](../status.md#status) · [ADRs](../adr/)
''')
        output = site.build(self.root)
        html = (output / "vision/index.html").read_text()
        self.assertIn('href="../index.html#a-section"', html)
        self.assertIn('href="../guide/index.html"', html)
        self.assertIn(f'href="{site.REPOSITORY}/blob/main/docs/status.md#status"', html)
        self.assertIn(f'href="{site.REPOSITORY}/tree/main/docs/adr"', html)

    def test_directory_readme_and_encoded_paths_resolve(self):
        (self.root / "docs/with spaces.md").write_text("# Space\n")
        self.brief.write_text('''# Jul.IA — Links

## A section

[Vision](./) · [Encoded](../with%20spaces.md?plain=1#space)
''')
        output = site.build(self.root)
        html = (output / "index.html").read_text()
        self.assertIn('href="vision/index.html"', html)
        self.assertIn("docs/with%20spaces.md?plain=1#space", html)

    def test_missing_markdown_target_blocks_publication(self):
        self.brief.write_text("# Title\n\n[Missing](missing.md)\n")
        with self.assertRaisesRegex(ValueError, "missing link target"):
            site.build(self.root)

    def test_repository_escape_blocks_publication(self):
        self.brief.write_text("# Title\n\n[Escape](../../../outside.md)\n")
        with self.assertRaisesRegex(ValueError, "leaves the repository"):
            site.build(self.root)

    def test_unsafe_and_protocol_relative_links_block_publication(self):
        for url in ["javascript:alert(1)", "//example.org/path"]:
            with self.subTest(url=url):
                self.brief.write_text(f"# Title\n\n[Unsafe]({url})\n")
                with self.assertRaises(ValueError):
                    site.build(self.root)

    def test_external_and_mail_links_remain_unchanged(self):
        self.brief.write_text('''# Title

[HTTPS](https://example.org/) · [HTTP](http://127.0.0.1:8080/) ·
[Mail](mailto:hello@example.org)
''')
        output = site.build(self.root)
        html = (output / "index.html").read_text()
        for url in ["https://example.org/", "http://127.0.0.1:8080/", "mailto:hello@example.org"]:
            self.assertIn(f'href="{url}"', html)

    def test_missing_document_title_blocks_publication(self):
        self.brief.write_text("## A section\n\nNo title.\n")
        with self.assertRaisesRegex(ValueError, "document title"):
            site.build(self.root)

    def test_broken_rendered_cross_page_and_local_anchors_block_publication(self):
        for link in ["README.md#missing", "#missing"]:
            with self.subTest(link=link):
                self.brief.write_text(f"# Title\n\n## A section\n\n[Broken]({link})\n")
                with self.assertRaisesRegex(ValueError, "broken output anchor"):
                    site.build(self.root)

    def test_missing_rendered_asset_and_duplicate_ids_block_publication(self):
        output = site.build(self.root)
        (output / "assets/site.css").unlink()
        with self.assertRaisesRegex(ValueError, "broken output link"):
            site.validate_output(output)
        output = site.build(self.root)
        file = output / "index.html"
        file.write_text(file.read_text() + '<div id="page-title"></div>')
        with self.assertRaisesRegex(ValueError, "duplicate HTML id"):
            site.validate_output(output)

    def test_raw_html_cannot_bypass_the_output_url_policy(self):
        for url in ["javascript:alert(1)", "//example.org/path"]:
            with self.subTest(url=url):
                self.brief.write_text(f'# Title\n\n<a href="{url}">Unsafe</a>\n')
                with self.assertRaises(ValueError):
                    site.build(self.root)

    def test_cli_reports_success_and_failure_with_the_correct_exit_status(self):
        with patch.object(site, "ROOT", self.root), patch.object(sys, "argv", ["build-site.py"]):
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(site.main(), 0)
            self.assertIn("Built and validated 6 canonical pages", output.getvalue())
            self.brief.unlink()
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(site.main(), 1)
            self.assertIn("Site build failed:", output.getvalue())



    def test_examples_images_and_phone_diagrams_publish_as_local_assets(self):
        page = next(page for page in site.PAGES if page.source == site.EXAMPLES_SOURCE)
        directory = self.root / "docs/examples/assets"
        directory.mkdir()
        for name in ("scene photo.webp", "sharing-map.svg", "sharing-map-mobile.svg"):
            (directory / name).write_bytes(name.encode())
        (self.root / page.source).write_text('''# Examples — My apps. My people.

Canonical introduction.

![An everyday scene](assets/scene%20photo.webp)

## One gateway

![Sharing map](assets/sharing-map.svg)
''')
        output = site.build(self.root)
        rendered = (output / page.output).read_text()
        self.assertIn('src="../assets/examples/scene%20photo.webp"', rendered)
        self.assertIn('srcset="../assets/examples/sharing-map-mobile.svg"', rendered)
        self.assertIn('alt="Sharing map"', rendered)
        self.assertEqual((output / "assets/examples/scene photo.webp").read_bytes(), b"scene photo.webp")
        (directory / "scene photo.webp").write_bytes(b"Updated picture")
        site.build(self.root)
        self.assertEqual((output / "assets/examples/scene photo.webp").read_bytes(), b"Updated picture")
        self.assertIn("Canonical introduction.", rendered)

    def test_missing_phone_diagram_blocks_publication(self):
        directory = self.root / "docs/examples/assets"
        directory.mkdir()
        (directory / "sensor-flow.svg").write_text("<svg/>")
        (self.root / site.EXAMPLES_SOURCE).write_text(
            "# Examples — Sensors\n\n## A section\n\n![Sensors](assets/sensor-flow.svg)\n")
        with self.assertRaisesRegex(ValueError, "broken output link"):
            site.build(self.root)

    def test_artwork_provenance_remains_a_repository_document(self):
        directory = self.root / "docs/examples/assets"
        directory.mkdir()
        (directory / "ARTWORK.md").write_text("# Artwork\n")
        (self.root / site.EXAMPLES_SOURCE).write_text(
            "# Examples — Artwork\n\n## A section\n\n[Artwork](assets/ARTWORK.md)\n")
        output = site.build(self.root)
        rendered = (output / "examples/index.html").read_text()
        self.assertIn(f"{site.REPOSITORY}/blob/main/docs/examples/assets/ARTWORK.md", rendered)
        self.assertFalse((output / "assets/examples/ARTWORK.md").exists())

    def test_all_pages_keep_existing_navigation_and_add_examples_after_vision(self):
        output = site.build(self.root)
        for page in site.PAGES:
            parser = site.Text()
            rendered = (output / page.output).read_text()
            start = rendered.index('<nav aria-label="Main navigation">')
            end = rendered.index("</nav>", start)
            parser.feed(rendered[start:end])
            labels = parser.parts
            self.assertEqual(labels, ["Our vision", "Examples", "Practical guide",
                                      "Documentation", "Get started", "GitHub ↗"])
            self.assertIn(f'href="{site.relative_url(page.output, "examples/index.html")}"', rendered)

    def test_raw_responsive_images_cannot_bypass_asset_validation(self):
        for value in ["missing.svg", "javascript:alert(1)", "//example.org/image.svg", "missing.svg,"]:
            with self.subTest(value=value):
                self.brief.write_text(f'# Title\n\n<source srcset="{value}">\n')
                with self.assertRaises(ValueError):
                    site.build(self.root)

if __name__ == "__main__":
    unittest.main()
