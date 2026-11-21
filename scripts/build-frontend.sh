#!/usr/bin/env bash
# Builds apps/chat-web from the pinned submodule third_party/chat (Node 22, its own lockfile and scripts) and copies the
# static site into frontend/dist, which the Go binary embeds. Nothing is edited in the submodule.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
git submodule update --init third_party/chat
major="$(node -p 'process.versions.node.split(".")[0]')"
[ "$major" = "22" ] || { echo "build-frontend: use Node 22 (found $(node -v))" >&2; exit 1; }
(
	cd third_party/chat
	npm ci --no-audit --no-fund
	npm run build
)
[ -n "$(git -C third_party/chat status --porcelain)" ] && { echo "build-frontend: the build changed tracked files in third_party/chat" >&2; git -C third_party/chat status --short >&2; exit 1; }
rm -rf frontend/dist
mkdir -p frontend/dist
cp -R third_party/chat/apps/chat-web/build/. frontend/dist/
touch frontend/dist/.gitkeep
echo "build-frontend: frontend/dist from chat $(git -C third_party/chat rev-parse --short HEAD)"
