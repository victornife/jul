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
        assets.mkdir()
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
            self.assertIn("Built and validated 5 canonical pages", output.getvalue())
            self.brief.unlink()
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(site.main(), 1)
            self.assertIn("Site build failed:", output.getvalue())


if __name__ == "__main__":
    unittest.main()
