#!/usr/bin/env python
# fix-test-config.py
import sys

with open('test-full.toml', 'r', encoding='utf-8') as f:
    content = f.read()

# Replace weighted server objects with plain strings
old_weighted = 'servers = [\n  { address = "127.0.0.1:50051", weight = 3 },\n  { address = "127.0.0.1:50052", weight = 1 }\n]'
new_plain = 'servers = ["127.0.0.1:50051", "127.0.0.1:50052"]'
content = content.replace(old_weighted, new_plain)

# Remove static-discovered upstream entirely
old_static = '''# Static discovery example
[[upstreams]]
name = "static-discovered"
strategy = "round_robin"
  [upstreams.discovery]
  type = "static"
  target = "127.0.0.1:3000"
  refresh = "30s"
'''
content = content.replace(old_static, '')

with open('test-full.toml', 'w', encoding='utf-8') as f:
    f.write(content)

print('OK')