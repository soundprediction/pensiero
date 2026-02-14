#!/bin/bash
set -e

# Get the project root directory
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

COZO_VERSION="v0.7.5"
PLATFORM="aarch64-apple-darwin" # For arm64 Mac
LIBRARY_NAME="libcozo_c"
OUTPUT_DIR="$PROJECT_ROOT/lib"

mkdir -p "$OUTPUT_DIR"

URL="https://github.com/cozodb/cozo/releases/download/${COZO_VERSION}/${LIBRARY_NAME}-${COZO_VERSION#v}-${PLATFORM}.a.gz"

if [ ! -f "$OUTPUT_DIR/${LIBRARY_NAME}.a.gz" ]; then
    echo "Downloading CozoDB C library from $URL..."
    curl -L -o "$OUTPUT_DIR/${LIBRARY_NAME}.a.gz" "$URL"
else
    echo "Using existing $OUTPUT_DIR/${LIBRARY_NAME}.a.gz"
fi

echo "Extracting library..."
gunzip -c "$OUTPUT_DIR/${LIBRARY_NAME}.a.gz" > "$OUTPUT_DIR/${LIBRARY_NAME}.a"

echo "Library prepared in $OUTPUT_DIR/${LIBRARY_NAME}.a"
