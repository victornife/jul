#!/usr/bin/env python3
from pathlib import Path

path = Path("internal/lifecycle/registry.go")
text = path.read_text()

old_reason = '''\treasonTracingStartup        = "the tracer provider and exporter are created once at startup"\n'''
new_reason = old_reason + '''\treasonTracingSampleRatioHot = "the stable ParentBased sampler delegates root decisions to an atomic ratio sampler updated at successful Publish; existing traces and parent decisions remain unchanged (#99)"\n'''
if old_reason not in text:
    raise SystemExit("reasonTracingStartup anchor not found")
text = text.replace(old_reason, new_reason, 1)

old_group = '''\tout = append(out, restartGroup(SubTracing, reasonTracingStartup,\n\t\t"observability.tracing.enabled",\n\t\t"observability.tracing.endpoint",\n\t\t"observability.tracing.exporter",\n\t\t"observability.tracing.insecure",\n\t\t"observability.tracing.sample_ratio",\n\t\t"observability.tracing.service_name",\n\t)...)\n'''
new_group = '''\tout = append(out, restartGroup(SubTracing, reasonTracingStartup,\n\t\t"observability.tracing.enabled",\n\t\t"observability.tracing.endpoint",\n\t\t"observability.tracing.exporter",\n\t\t"observability.tracing.insecure",\n\t\t"observability.tracing.service_name",\n\t)...)\n\tout = append(out, hot("observability.tracing.sample_ratio", SubTracing, reasonTracingSampleRatioHot))\n'''
if old_group not in text:
    raise SystemExit("tracing lifecycle group anchor not found")
text = text.replace(old_group, new_group, 1)
path.write_text(text)
