# upscale

Upscale images and videos with a fast, local-first CLI built around proven open-source models.

`upscale` gives you a simple command surface for common enhancement workflows, whether you are restoring old footage, improving anime content, or preparing visuals for higher-resolution displays.

## Why upscale

- One CLI for both image and video upscaling
- Multiple engines for different content styles
- Local processing by default
- Cross-platform tool bundles included in this repository
- Practical progress output suitable for terminal and GUI wrappers

## What You Can Do

- Improve image clarity for photos and illustrations
- Increase video resolution while preserving audio tracks
- Choose processing style based on content type
- Switch between quality levels depending on speed vs fidelity needs

## Engines At A Glance

| Engine | Best for | Typical use |
| --- | --- | --- |
| `realesrgan` | General image enhancement | Photos, scans, digital art |
| `swinir` | High-detail image restoration | Quality-focused still images |
| `realesrgan-video` | General video upscaling | Mixed live-action and animation |
| `anime4k-video` | Anime and line art animation | Sharp edges, clean stylized content |

## Install

### Option 1: Build from source

Requirements:

- Go
- FFmpeg

Build:

```bash
go build -o upscale .
```

### Option 2: Arch Linux package

This repository includes packaging files for Arch-based distributions.

## Quick Start

General command shape:

```bash
upscale <image|video> -i <input> -o <output> [options]
```

### Image example

```bash
./upscale image -i input.jpg -o output.png -a realesrgan -m normal -q 3
```

### Video example

```bash
./upscale video -i input.mp4 -o output.mp4 -a anime4k-video -s 2 -q 3
```

## Common Options

- `-a`: Select engine/adapter
- `-s`: Upscale factor
- `-q`: Quality preset (`1` low, `2` medium, `3` high, `4` ultra)
- `-m`: Content style (`normal` or `anime`)
- `-p`: JSON for engine-specific tuning
- `-r`: Raw progress output (useful for wrappers and scripts)

Run help for all options:

```bash
./upscale image -h
./upscale video -h
```

## Typical Workflows

### General photos

Use `realesrgan` with `-m normal`.

### Anime frames and clips

Use `anime4k-video` for video and `realesrgan` with `-m anime` for still images.

### Maximum still-image quality

Try `swinir` with higher quality presets.

## Project Goals

`upscale` focuses on being:

- Easy to run from terminal scripts
- Friendly to automation and batch jobs
- Flexible enough to swap engines without changing your workflow

## License

MIT
