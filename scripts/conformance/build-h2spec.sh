#!/bin/sh
set -eu

source_dir=$1
output=$2
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
patch_file="$script_dir/h2spec-early-response.patch"
commit=70ac2294010887f48b18e2d64f5cccd48421fad1
patch_sha=$(sha256sum "$patch_file" | cut -d ' ' -f 1)

cd "$source_dir"
if [ "$(git rev-parse HEAD)" != "$commit" ]; then
    echo "h2spec must be built from v2.6.0 commit $commit" >&2
    exit 1
fi
git --no-pager diff --exit-code
git --no-pager diff --cached --exit-code
git apply --check "$patch_file"
git apply "$patch_file"
go test -race -count=1 ./spec ./http2
go build -ldflags "-X main.VERSION=2.6.0+jul.early-response.1 -X main.COMMIT=$commit+patch.$patch_sha" \
    -o "$output" ./cmd/h2spec
