#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go build -buildvcs=false -o notes
cp notes /usr/local/notes/
