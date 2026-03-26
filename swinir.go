package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	swinIRScale       = 4
	swinIRDefaultTile = 512
)

type SwinIRAdapter struct {
	toolsDir string
}

func NewSwinIRAdapter(toolsDir string) *SwinIRAdapter {
	return &SwinIRAdapter{toolsDir: toolsDir}
}

func (a *SwinIRAdapter) Name() string {
	return "swinir"
}

func (a *SwinIRAdapter) Capabilities() Capabilities {
	return Capabilities{
		SupportsDenoise: false,
		SupportsPrompt:  false,
		SupportsFaces:   false,
	}
}

func (a *SwinIRAdapter) Prepare(params UpscaleParams) map[string]any {
	if params.Scale != swinIRScale {
		log.Warn().Str("adapter", a.Name()).Int("requested_scale", params.Scale).Msg("adapter only supports 4x upscaling, ignoring requested scale")
	}

	useLargeModel := params.Quality >= QualityHigh
	if customLargeModel, ok := adapterParamBool(params, "large_model", "large"); ok {
		useLargeModel = customLargeModel
	}

	modelPath := resolveSwinIRMediumModel(a.toolsDir)
	if useLargeModel {
		modelPath = resolveSwinIRLargeModel(a.toolsDir)
	}
	if customModel, ok := adapterParamString(params, "model"); ok {
		modelPath = customModel
		if _, hasLargeOverride := adapterParamBool(params, "large_model", "large"); !hasLargeOverride {
			useLargeModel = inferSwinIRLargeModel(customModel)
		}
	}

	tile := swinIRDefaultTile
	if customTile, ok := adapterParamInt(params, "tile"); ok {
		tile = customTile
	}

	tileOverlap := 32
	if customOverlap, ok := adapterParamInt(params, "tile_overlap"); ok {
		tileOverlap = customOverlap
	}

	pythonBinary := resolveSwinIRPython(a.toolsDir)
	if customPython, ok := adapterParamString(params, "python", "python_bin"); ok {
		pythonBinary = customPython
	}

	return map[string]any{
		"python":       pythonBinary,
		"script":       resolveSwinIRScript(a.toolsDir),
		"model_path":   modelPath,
		"scale":        swinIRScale,
		"large_model":  useLargeModel,
		"tile":         tile,
		"tile_overlap": tileOverlap,
	}
}

func (a *SwinIRAdapter) Run(ctx context.Context, job *Job) error {
	cfg := a.Prepare(job.Params)

	log.Warn().Msg("swinir adapter is experimental and may fail!")

	pythonBinary := cfg["python"].(string)
	scriptPath := cfg["script"].(string)
	modelPath := cfg["model_path"].(string)
	if isPathLike(pythonBinary) && !filepath.IsAbs(pythonBinary) {
		abs, err := filepath.Abs(pythonBinary)
		if err != nil {
			return fmt.Errorf("failed to resolve python path: %w", err)
		}
		pythonBinary = abs
	}
	if !filepath.IsAbs(scriptPath) {
		abs, err := filepath.Abs(scriptPath)
		if err != nil {
			return fmt.Errorf("failed to resolve SwinIR script path: %w", err)
		}
		scriptPath = abs
	}
	if !filepath.IsAbs(modelPath) {
		abs, err := filepath.Abs(modelPath)
		if err != nil {
			return fmt.Errorf("failed to resolve SwinIR model path: %w", err)
		}
		modelPath = abs
	}
	scale := cfg["scale"].(int)
	useLargeModel := cfg["large_model"].(bool)
	tile := cfg["tile"].(int)
	tileOverlap := cfg["tile_overlap"].(int)

	workDir, err := os.MkdirTemp("", "upscale-swinir-*")
	if err != nil {
		return fmt.Errorf("failed to create temp work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	inputDir := filepath.Join(workDir, "input")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create temp input dir: %w", err)
	}

	inputBase := filepath.Base(job.InputPath)
	stagedInput := filepath.Join(inputDir, inputBase)
	if err := copyFile(job.InputPath, stagedInput); err != nil {
		return fmt.Errorf("failed to stage input image: %w", err)
	}

	args := []string{
		scriptPath,
		"--task", "real_sr",
		"--scale", strconv.Itoa(scale),
		"--model_path", modelPath,
		"--folder_lq", inputDir,
	}
	if useLargeModel {
		args = append(args, "--large_model")
	}
	if tile > 0 {
		args = append(args, "--tile", strconv.Itoa(tile))
	}
	if tileOverlap > 0 {
		args = append(args, "--tile_overlap", strconv.Itoa(tileOverlap))
	}

	job.Events <- Event{
		Type: EventLog,
		Message: fmt.Sprintf(
			"adapter=%s python=%s model=%s scale=%dx large_model=%t input=%s output=%s",
			a.Name(), pythonBinary, filepath.Base(modelPath), scale, useLargeModel, job.InputPath, job.OutputPath,
		),
	}
	job.Events <- Event{Type: EventProgress, Progress: 0.05}

	stderrLines, err := runSwinIRCommand(ctx, job, pythonBinary, workDir, args)
	if err != nil {
		if shouldRetryWithVenv(stderrLines) {
			if venvPython, ok := resolveSwinIRVenvPython(a.toolsDir); ok {
				if isPathLike(venvPython) && !filepath.IsAbs(venvPython) {
					abs, absErr := filepath.Abs(venvPython)
					if absErr != nil {
						return fmt.Errorf("failed to resolve SwinIR venv python path: %w", absErr)
					}
					venvPython = abs
				}
				if venvPython != pythonBinary {
					job.Events <- Event{Type: EventLog, Message: "system python is missing SwinIR deps; retrying with SwinIR venv"}
					job.Events <- Event{Type: EventLog, Message: fmt.Sprintf("retry python=%s", venvPython)}
					stderrLines, err = runSwinIRCommand(ctx, job, venvPython, workDir, args)
				}
			}
		}
	}
	if err != nil {
		if len(stderrLines) > 0 {
			return fmt.Errorf("swinir failed: %w: %s", err, strings.Join(stderrLines, " | "))
		}
		return fmt.Errorf("swinir failed: %w", err)
	}

	resultPath, err := findSwinIROutput(workDir, inputBase, useLargeModel)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(job.OutputPath), 0o755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}
	if err := writeImageOutput(resultPath, job.OutputPath); err != nil {
		return err
	}

	job.Events <- Event{Type: EventProgress, Progress: 1.0}
	return nil
}

func inferSwinIRLargeModel(modelPath string) bool {
	name := strings.ToLower(filepath.Base(modelPath))
	return strings.Contains(name, "swinir-l") || strings.Contains(name, "_l_")
}

func findSwinIROutput(workDir, inputBase string, useLargeModel bool) (string, error) {
	imgName := strings.TrimSuffix(filepath.Base(inputBase), filepath.Ext(inputBase))
	saveDir := filepath.Join(workDir, "results", fmt.Sprintf("swinir_real_sr_x%d", swinIRScale))
	if useLargeModel {
		saveDir += "_large"
	}
	expected := filepath.Join(saveDir, imgName+"_SwinIR.png")
	if _, err := os.Stat(expected); err == nil {
		return expected, nil
	}

	matches, err := filepath.Glob(filepath.Join(workDir, "results", "*", imgName+"_SwinIR.png"))
	if err == nil && len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("swinir finished but output image was not found")
}

func writeImageOutput(srcPath, dstPath string) error {
	ext := strings.ToLower(filepath.Ext(dstPath))
	if ext == "" || ext == ".png" {
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("failed to write output image: %w", err)
		}
		return nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open SwinIR output: %w", err)
	}
	defer src.Close()

	img, _, err := image.Decode(src)
	if err != nil {
		return fmt.Errorf("failed to decode SwinIR output: %w", err)
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create output image: %w", err)
	}
	defer dst.Close()

	switch ext {
	case ".jpg", ".jpeg":
		if err := jpeg.Encode(dst, img, &jpeg.Options{Quality: 95}); err != nil {
			return fmt.Errorf("failed to encode jpeg output: %w", err)
		}
	case ".png":
		if err := png.Encode(dst, img); err != nil {
			return fmt.Errorf("failed to encode png output: %w", err)
		}
	default:
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("failed to write output image: %w", err)
		}
	}

	return nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Close()
}

func runSwinIRCommand(ctx context.Context, job *Job, pythonBinary, workDir string, args []string) ([]string, error) {
	cmd := exec.CommandContext(ctx, pythonBinary, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start SwinIR: %w", err)
	}

	var stderrLines []string
	done := make(chan struct{}, 2)

	parseLine := func(line string, isStderr bool) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		if isStderr {
			stderrLines = append(stderrLines, line)
		}
		switch {
		case strings.HasPrefix(line, "loading model from"):
			job.Events <- Event{Type: EventLog, Message: line}
			job.Events <- Event{Type: EventProgress, Progress: 0.15}
		case strings.HasPrefix(line, "Testing "):
			job.Events <- Event{Type: EventLog, Message: line}
			job.Events <- Event{Type: EventProgress, Progress: 0.8}
		default:
			job.Events <- Event{Type: EventLog, Message: line}
		}
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			parseLine(scanner.Text(), false)
		}
		done <- struct{}{}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			parseLine(scanner.Text(), true)
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if err := cmd.Wait(); err != nil {
		return stderrLines, err
	}

	return stderrLines, nil
}

func shouldRetryWithVenv(stderrLines []string) bool {
	if len(stderrLines) == 0 {
		return false
	}
	joined := strings.ToLower(strings.Join(stderrLines, "\n"))
	if strings.Contains(joined, "modulenotfounderror") {
		return true
	}
	if strings.Contains(joined, "no module named") {
		return true
	}
	return false
}

func isPathLike(s string) bool {
	return strings.ContainsRune(s, filepath.Separator) || strings.Contains(s, "/") || strings.Contains(s, "\\")
}
