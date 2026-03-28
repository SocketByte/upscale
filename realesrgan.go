package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	ModelRealESRGANx4Plus      = "realesrgan-x4plus"
	ModelRealESRGANx4PlusAnime = "realesrgan-x4plus-anime"
	ModelAnimeVideoV3x2        = "realesr-animevideov3-x2"
	ModelAnimeVideoV3x3        = "realesr-animevideov3-x3"
	ModelAnimeVideoV3x4        = "realesr-animevideov3-x4"
)

type RealESRGANAdapter struct {
	toolsDir string
}

func NewRealESRGANAdapter(toolsDir string) *RealESRGANAdapter {
	return &RealESRGANAdapter{toolsDir: toolsDir}
}

func (a *RealESRGANAdapter) Name() string {
	return "realesrgan"
}

func (a *RealESRGANAdapter) Capabilities() Capabilities {
	return Capabilities{
		SupportsDenoise: false,
		SupportsPrompt:  false,
		SupportsFaces:   false,
	}
}

func (a *RealESRGANAdapter) binaryPath() string {
	return resolveRealESRGANBinary(a.toolsDir)
}

func (a *RealESRGANAdapter) modelsPath() string {
	return resolveRealESRGANModelsDir(a.toolsDir)
}

func selectModel(params UpscaleParams) string {
	switch params.Modifier {
	case ModifierAnime:
		return ModelRealESRGANx4PlusAnime
	case ModifierNormal:
		return ModelRealESRGANx4Plus
	default:
		return ModelRealESRGANx4Plus
	}
}

func (a *RealESRGANAdapter) Prepare(params UpscaleParams) map[string]any {
	model := selectModel(params)
	if customModel, ok := adapterParamString(params, "model"); ok {
		model = customModel
	}

	if params.Scale != 4 {
		log.Warn().Str("adapter", a.Name()).Int("requested_scale", params.Scale).Msg("adapter only supports 4x upscaling, ignoring requested scale")
	}
	scale := 4

	return map[string]any{
		"binary":      a.binaryPath(),
		"models_path": a.modelsPath(),
		"model":       model,
		"scale":       scale,
	}
}

func (a *RealESRGANAdapter) Run(ctx context.Context, job *Job) error {
	cfg := a.Prepare(job.Params)

	binary := cfg["binary"].(string)
	model := cfg["model"].(string)
	scale := cfg["scale"].(int)

	args := []string{
		"-i", job.InputPath,
		"-o", job.OutputPath,
		"-n", model,
		"-s", strconv.Itoa(scale),
	}
	if shouldPassRealESRGANModelsDir(binary) {
		modelsPath := cfg["models_path"].(string)
		args = append(args, "-m", modelsPath)
	}

	job.Events <- Event{
		Type:    EventLog,
		Message: fmt.Sprintf("adapter=%s model=%s scale=%dx input=%s output=%s", a.Name(), model, scale, job.InputPath, job.OutputPath),
	}

	cmd := exec.CommandContext(ctx, binary, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start realesrgan-ncnn-vulkan: %w", err)
	}

	parseLine := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		if strings.HasSuffix(line, "%") {
			pct, err := strconv.ParseFloat(strings.TrimSuffix(line, "%"), 64)
			if err == nil {
				job.Events <- Event{Type: EventProgress, Progress: pct / 100.0}
				return
			}
		}
	}

	done := make(chan struct{}, 2)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			parseLine(scanner.Text())
		}
		done <- struct{}{}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			parseLine(scanner.Text())
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("realesrgan-ncnn-vulkan failed: %w", err)
	}

	job.Events <- Event{Type: EventProgress, Progress: 1.0}
	return nil
}
