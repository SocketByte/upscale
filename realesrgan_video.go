package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

type RealESRGANVideoAdapter struct {
	toolsDir string
}

func NewRealESRGANVideoAdapter(toolsDir string) *RealESRGANVideoAdapter {
	return &RealESRGANVideoAdapter{toolsDir: toolsDir}
}

func (a *RealESRGANVideoAdapter) Name() string {
	return "realesrgan-video"
}

func (a *RealESRGANVideoAdapter) Capabilities() Capabilities {
	return Capabilities{}
}

func (a *RealESRGANVideoAdapter) esrganBinary() string {
	return resolveRealESRGANBinary(a.toolsDir)
}

func (a *RealESRGANVideoAdapter) esrganModels() string {
	return resolveRealESRGANModelsDir(a.toolsDir)
}

func (a *RealESRGANVideoAdapter) ffmpegBinary() string {
	return resolveFFmpegBinary(a.toolsDir)
}

func selectVideoModel(params UpscaleParams) string {
	switch params.Scale {
	case 2:
		return ModelAnimeVideoV3x2
	case 3:
		return ModelAnimeVideoV3x3
	default:
		switch params.Modifier {
		case ModifierAnime:
			log.Warn().Msg("for anime based content, you may try using anime4k adapter instead")
			return ModelAnimeVideoV3x4
		default:
			return ModelAnimeVideoV3x4
		}
	}
}

func (a *RealESRGANVideoAdapter) Prepare(params UpscaleParams) map[string]any {
	model := selectVideoModel(params)
	if customModel, ok := adapterParamString(params, "model"); ok {
		model = customModel
	}
	scale := params.Scale
	if scale < 2 || scale > 4 {
		scale = 4
	}
	return map[string]any{
		"model": model,
		"scale": scale,
	}
}

func (a *RealESRGANVideoAdapter) Run(ctx context.Context, job *Job) error {
	cfg := a.Prepare(job.Params)
	model := cfg["model"].(string)
	scale := cfg["scale"].(int)

	tmpDir, err := os.MkdirTemp("", "upscale-video-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	framesDir := filepath.Join(tmpDir, "frames")
	upscaledDir := filepath.Join(tmpDir, "upscaled")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(upscaledDir, 0o755); err != nil {
		return err
	}

	ffmpeg := a.ffmpegBinary()
	esrgan := a.esrganBinary()

	fps, err := a.probeVideoFPS(ctx, ffmpeg, job.InputPath)
	if err != nil {
		return fmt.Errorf("failed to probe video fps: %w", err)
	}

	job.Events <- Event{
		Type:    EventLog,
		Message: fmt.Sprintf("adapter=%s model=%s scale=%dx fps=%.3g", a.Name(), model, scale, fps),
	}

	job.Events <- Event{Type: EventLog, Message: "extracting frames"}
	if err := a.extractFrames(ctx, ffmpeg, job.InputPath, framesDir); err != nil {
		return fmt.Errorf("frame extraction failed: %w", err)
	}

	entries, err := os.ReadDir(framesDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no frames extracted from video")
	}
	job.Events <- Event{Type: EventLog, Message: fmt.Sprintf("extracted %d frames", len(entries))}

	job.Events <- Event{Type: EventLog, Message: "upscaling frames"}
	if err := a.upscaleFrames(ctx, esrgan, framesDir, upscaledDir, model, scale, job); err != nil {
		return fmt.Errorf("frame upscaling failed: %w", err)
	}

	job.Events <- Event{Type: EventLog, Message: "assembling video"}
	if err := a.assembleVideo(ctx, ffmpeg, upscaledDir, job.InputPath, job.OutputPath, fps); err != nil {
		return fmt.Errorf("video assembly failed: %w", err)
	}

	job.Events <- Event{Type: EventProgress, Progress: 1.0}
	return nil
}

func (a *RealESRGANVideoAdapter) probeVideoFPS(ctx context.Context, ffmpeg, input string) (float64, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-i", input)
	out, _ := cmd.CombinedOutput()
	s := string(out)

	for _, pattern := range []string{`(\d+(?:\.\d+)?) fps`, `(\d+(?:\.\d+)?) tbr`} {
		re := regexp.MustCompile(pattern)
		if m := re.FindStringSubmatch(s); len(m) > 1 {
			if fps, err := strconv.ParseFloat(m[1], 64); err == nil && fps > 0 {
				return fps, nil
			}
		}
	}
	return 30, nil
}

func (a *RealESRGANVideoAdapter) extractFrames(ctx context.Context, ffmpeg, input, framesDir string) error {
	args := []string{
		"-i", input,
		"-qscale:v", "1",
		"-qmin", "1",
		"-y",
		filepath.Join(framesDir, "%08d.png"),
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
	}
	return cmd.Wait()
}

func (a *RealESRGANVideoAdapter) upscaleFrames(ctx context.Context, esrgan, framesDir, upscaledDir, model string, scale int, job *Job) error {
	args := []string{
		"-i", framesDir,
		"-o", upscaledDir,
		"-n", model,
		"-s", strconv.Itoa(scale),
		"-m", a.esrganModels(),
	}
	cmd := exec.CommandContext(ctx, esrgan, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start realesrgan-ncnn-vulkan: %w", err)
	}

	parse := func(line string) {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "%") {
			pct, err := strconv.ParseFloat(strings.TrimSuffix(line, "%"), 64)
			if err == nil {
				job.Events <- Event{Type: EventProgress, Progress: (pct / 100.0) * 0.9}
			}
		}
	}

	done := make(chan struct{}, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			parse(scanner.Text())
		}
		done <- struct{}{}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			parse(scanner.Text())
		}
		done <- struct{}{}
	}()
	<-done
	<-done

	return cmd.Wait()
}

func (a *RealESRGANVideoAdapter) assembleVideo(ctx context.Context, ffmpeg, upscaledDir, originalInput, output string, fps float64) error {
	fpsStr := strconv.FormatFloat(fps, 'f', -1, 64)
	args := []string{
		"-framerate", fpsStr,
		"-i", filepath.Join(upscaledDir, "%08d.png"),
		"-i", originalInput,
		"-map", "0:v:0",
		"-map", "1:a?",
		"-c:v", "libx264",
		"-crf", "18",
		"-preset", "slow",
		"-c:a", "copy",
		"-movflags", "+faststart",
		"-y",
		output,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
	}
	return cmd.Wait()
}
