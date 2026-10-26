#!/usr/bin/env bash
# Builds the diskusage CLI and drops it into the app bundle's Resources, so
# the shipped .app is self-contained and the GUI always has a matching
# binary to shell out to. Run as an Xcode "Run Script" build phase.
set -euo pipefail

REPO_ROOT="${SRCROOT}/.."
DEST_DIR="${BUILT_PRODUCTS_DIR}/${UNLOCALIZED_RESOURCES_FOLDER_PATH}"
mkdir -p "${DEST_DIR}"

GO_BIN="$(command -v go || true)"
if [ -z "${GO_BIN}" ]; then
	for candidate in /usr/local/go/bin/go /opt/homebrew/bin/go /usr/local/bin/go "${HOME}/go/bin/go"; do
		if [ -x "${candidate}" ]; then
			GO_BIN="${candidate}"
			break
		fi
	done
fi
if [ -z "${GO_BIN}" ]; then
	echo "error: Go toolchain not found. Install it from https://go.dev/dl/, make sure 'go' is on PATH, then rebuild." >&2
	exit 1
fi

echo "Building diskusage CLI with: ${GO_BIN}"
( cd "${REPO_ROOT}" && "${GO_BIN}" build -o "${DEST_DIR}/diskusage" . )
