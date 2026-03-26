#!/usr/bin/env bash
set -euo pipefail

mkdir -p "$(dirname "$0")/tools/pytorch"
PYTORCH_DIR="$(cd "$(dirname "$0")/tools/pytorch" && pwd)"
SWINIR_DIR="$PYTORCH_DIR/SwinIR"
REPO_URL="https://github.com/JingyunLiang/SwinIR.git"

if [ -d "$SWINIR_DIR/.git" ]; then
    echo "SwinIR already cloned, skipping."
else
    echo "Cloning SwinIR..."
    git clone "$REPO_URL" "$SWINIR_DIR"
fi

cd "$SWINIR_DIR"

if [ ! -d "venv" ]; then
    echo "Creating Python virtual environment..."
    python3 -m venv venv
fi

echo "Installing dependencies..."
venv/bin/pip install --upgrade pip
venv/bin/pip install torch torchvision numpy opencv-python timm requests

echo "Done. SwinIR is ready in $SWINIR_DIR"
