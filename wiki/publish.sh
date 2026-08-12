#!/usr/bin/env bash
# Publiziert wiki/*.md ins GitHub-Wiki. Aus dem Repo-Root ausführen: bash wiki/publish.sh
# Voraussetzung: das Wiki ist im Repo aktiviert und enthält mindestens eine Seite
# (GitHub legt das .wiki.git erst mit der ersten, über die Weboberfläche erstellten Seite an).
set -Eeuo pipefail

REPO="${WIKI_REPO:-https://github.com/Elias02345/remote-agent.wiki.git}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLONE="$SRC/../.wiki-clone"

rm -rf "$CLONE"
git clone --depth 1 "$REPO" "$CLONE"
find "$CLONE" -maxdepth 1 -name '*.md' -delete
cp "$SRC"/*.md "$CLONE/"
git -C "$CLONE" add -A
git -C "$CLONE" diff --cached --quiet && { echo "Wiki already up to date."; exit 0; }
git -C "$CLONE" commit -m "Update wiki from wiki/"
git -C "$CLONE" push
echo "Wiki published."
