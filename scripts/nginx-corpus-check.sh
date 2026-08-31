#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

go run -tags importer scripts/nginx-corpus-report.go -check
go test -tags importer ./internal/migrate/nginx/corpus
go test -tags importer ./cmd/jul \
  -run '^TestNGINXCorpusAssessmentCandidateAndRealJul$' \
  -count=1
