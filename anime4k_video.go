package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type Anime4KVideoAdapter struct {
	toolsDir string
}

type mediaBackend struct {
	name        string
	decoderArgs []string
	encoderArgs []string
}

func NewAnime4KVideoAdapter(toolsDir string) *Anime4KVideoAdapter {
	return &Anime4KVideoAdapter{toolsDir: toolsDir}
}

func (a *Anime4KVideoAdapter) Name() string { return "anime4k-video" }

func (a *Anime4KVideoAdapter) Capabilities() Capabilities { return Capabilities{} }

func (a *Anime4KVideoAdapter) bundledFFmpeg() string {
	return resolveFFmpegBinary(a.toolsDir)
}

func (a *Anime4KVideoAdapter) resolveFFmpeg(ctx context.Context) (string, error) {
	candidates := []string{a.bundledFFmpeg(), "ffmpeg"}
	for _, candidate := range candidates {
		if a.hasLibplacebo(ctx, candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no ffmpeg with libplacebo support found")
}

func (a *Anime4KVideoAdapter) hasLibplacebo(ctx context.Context, ffmpeg string) bool {
	cmd := exec.CommandContext(ctx, ffmpeg, "-filters")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "libplacebo")
}

func (a *Anime4KVideoAdapter) shadersDir() string {
	return filepath.Join(a.toolsDir, "anime4k")
}

func (a *Anime4KVideoAdapter) shader(name string) string {
	return filepath.Join(a.shadersDir(), name)
}

func qualitySize(quality int) string {
	switch quality {
	case QualityLow:
		return "S"
	case QualityMedium:
		return "M"
	case QualityHigh:
		return "L"
	default:
		return "UL"
	}
}

func (a *Anime4KVideoAdapter) selectShaders(params UpscaleParams) []string {
	size := qualitySize(params.Quality)
	s := a.shader

	mode := a.effectiveMode(params)
	if mode == "" {
		mode = "a"
	}

	var firstPass []string
	switch mode {
	case "b":
		firstPass = []string{
			s("Anime4K_Clamp_Highlights.glsl"),
			s(fmt.Sprintf("Anime4K_Upscale_Denoise_CNN_x2_%s.glsl", size)),
			s(fmt.Sprintf("Anime4K_Restore_CNN_Soft_%s.glsl", size)),
		}
	default:
		firstPass = []string{
			s("Anime4K_Clamp_Highlights.glsl"),
			s(fmt.Sprintf("Anime4K_Restore_CNN_Soft_%s.glsl", size)),
			s(fmt.Sprintf("Anime4K_Upscale_CNN_x2_%s.glsl", size)),
		}
	}

	if params.Scale <= 2 {
		return firstPass
	}

	return append(firstPass,
		s("Anime4K_AutoDownscalePre_x2.glsl"),
		s("Anime4K_AutoDownscalePre_x4.glsl"),
		s("Anime4K_Restore_CNN_Soft_S.glsl"),
		s("Anime4K_Upscale_CNN_x2_S.glsl"),
	)
}

func (a *Anime4KVideoAdapter) Prepare(params UpscaleParams) map[string]any {
	scale := params.Scale
	if scale != 2 && scale != 4 {
		scale = 2
	}
	return map[string]any{
		"scale":   scale,
		"shaders": a.selectShaders(params),
	}
}

func (a *Anime4KVideoAdapter) Run(ctx context.Context, job *Job) error {
	cfg := a.Prepare(job.Params)
	scale := cfg["scale"].(int)
	shaders := cfg["shaders"].([]string)

	ffmpeg, err := a.resolveFFmpeg(ctx)
	if err != nil {
		return err
	}

	job.Events <- Event{
		Type: EventLog,
		Message: fmt.Sprintf(
			"adapter=%s mode=%s scale=%dx quality=%d shaders=%d",
			a.Name(), a.effectiveMode(job.Params), scale, job.Params.Quality, len(shaders),
		),
	}

	combinedShaderPath, cleanup, err := a.combineShaders(shaders)
	if err != nil {
		return err
	}
	defer cleanup()

	filter := a.buildFilter(scale, combinedShaderPath)
	backend := a.selectMediaBackend(ctx, ffmpeg, job.Params)

	durationSec, _ := a.probeDurationSeconds(ctx, ffmpeg, job.InputPath)
	totalFrames, _ := a.probeFrameCount(ctx, ffmpeg, job.InputPath)

	job.Events <- Event{Type: EventLog, Message: "processing video with anime4k shaders (ffmpeg libplacebo)"}
	job.Events <- Event{Type: EventLog, Message: fmt.Sprintf("media backend=%s", backend.name)}

	args := append([]string{}, backend.decoderArgs...)
	args = append(args,
		"-i", job.InputPath,
		"-vf", filter,
		"-progress", "pipe:2",
		"-nostats",
		"-map", "0:v:0",
		"-map", "0:a?",
	)
	args = append(args, backend.encoderArgs...)
	args = append(args,
		"-c:a", "copy",
		"-movflags", "+faststart",
		"-y",
		job.OutputPath,
	)

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w (ensure ffmpeg is built with libplacebo support)", err)
	}

	frameRe := regexp.MustCompile(`frame=\s*(\d+)`)
	lastProgress := -1.0
	var stderrLines []string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "out_time=") && durationSec > 0 {
			t := strings.TrimPrefix(line, "out_time=")
			if sec, err := parseFFmpegClock(t); err == nil && sec >= 0 {
				progress := sec / durationSec
				if progress > 1.0 {
					progress = 1.0
				}
				emit := progress * 0.99
				if emit > lastProgress {
					job.Events <- Event{Type: EventProgress, Progress: emit}
					lastProgress = emit
				}
			}
		}

		stderrLines = append(stderrLines, line)
		if len(stderrLines) > 25 {
			stderrLines = stderrLines[1:]
		}

		if totalFrames <= 0 {
			continue
		}
		if m := frameRe.FindStringSubmatch(line); len(m) > 1 {
			frame, _ := strconv.ParseInt(m[1], 10, 64)
			progress := float64(frame) / float64(totalFrames)
			if progress > 1.0 {
				progress = 1.0
			}
			emit := progress * 0.99
			if emit > lastProgress {
				job.Events <- Event{Type: EventProgress, Progress: emit}
				lastProgress = emit
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading ffmpeg stderr failed: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if len(stderrLines) == 0 {
			return fmt.Errorf("ffmpeg failed: %w", err)
		}
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.Join(stderrLines, " | "))
	}

	job.Events <- Event{Type: EventProgress, Progress: 1.0}
	return nil
}

func (a *Anime4KVideoAdapter) effectiveMode(params UpscaleParams) string {
	if mode, ok := adapterParamString(params, "mode"); ok {
		if strings.ToLower(mode) == "b" {
			return "b"
		}
		return "a"
	}
	return "a"
}

func (a *Anime4KVideoAdapter) buildFilter(scale int, shaderPath string) string {
	opts := []string{
		fmt.Sprintf("w=iw*%d", scale),
		fmt.Sprintf("h=ih*%d", scale),
		"custom_shader_path=" + escapeFilterValue(shaderPath),
	}

	return "libplacebo=" + strings.Join(opts, ":")
}

func (a *Anime4KVideoAdapter) combineShaders(shaderPaths []string) (string, func(), error) {
	if len(shaderPaths) == 0 {
		return "", func() {}, fmt.Errorf("no anime4k shaders were selected")
	}

	f, err := os.CreateTemp("", "anime4k-shader-*.glsl")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temporary shader file: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(f.Name())
	}

	for _, p := range shaderPaths {
		content, err := os.ReadFile(p)
		if err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("failed to read shader %q: %w", p, err)
		}
		if _, err := f.WriteString("\n--- " + filepath.Base(p) + " ---\n"); err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("failed to write shader header: %w", err)
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("failed to write shader body: %w", err)
		}
		if _, err := f.WriteString("\n"); err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("failed to write shader separator: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to finalize temporary shader file: %w", err)
	}

	return f.Name(), cleanup, nil
}

func escapeFilterValue(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`:`, `\:`,
		`,`, `\,`,
		`;`, `\;`,
		`[`, `\[`,
		`]`, `\]`,
		`=`, `\=`,
	)
	return replacer.Replace(value)
}

func parseFFmpegClock(v string) (float64, error) {
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid ffmpeg out_time format: %q", v)
	}
	h, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	m, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, err
	}
	s, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, err
	}
	return h*3600 + m*60 + s, nil
}

func (a *Anime4KVideoAdapter) probeDurationSeconds(ctx context.Context, ffmpeg, input string) (float64, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-i", input)
	out, _ := cmd.CombinedOutput()
	s := string(out)

	durRe := regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
	m := durRe.FindStringSubmatch(s)
	if len(m) <= 3 {
		return 0, fmt.Errorf("could not determine duration from ffmpeg probe output")
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	min, _ := strconv.ParseFloat(m[2], 64)
	sec, _ := strconv.ParseFloat(m[3], 64)
	return h*3600 + min*60 + sec, nil
}

func (a *Anime4KVideoAdapter) selectMediaBackend(ctx context.Context, ffmpeg string, params UpscaleParams) mediaBackend {
	vendor := detectGPUVendor()
	encodersOut := strings.ToLower(a.ffmpegOutput(ctx, ffmpeg, "-encoders"))
	hwaccelsOut := strings.ToLower(a.ffmpegOutput(ctx, ffmpeg, "-hwaccels"))
	preferred := strings.ToLower(strings.TrimSpace(func() string {
		if s, ok := adapterParamString(params, "encoder", "codec"); ok {
			return s
		}
		return ""
	}()))

	hasEncoder := func(name string) bool {
		return strings.Contains(encodersOut, strings.ToLower(name))
	}

	wants := func(family string) bool {
		if preferred == "" || preferred == "auto" {
			return true
		}
		switch family {
		case "av1":
			return preferred == "av1" || strings.HasPrefix(preferred, "av1_")
		case "hevc":
			return preferred == "hevc" || preferred == "h265" || strings.HasPrefix(preferred, "hevc_")
		case "h264":
			return preferred == "h264" || preferred == "x264" || strings.HasPrefix(preferred, "h264_")
		default:
			return false
		}
	}

	matchesExact := func(codec string) bool {
		if preferred == "" || preferred == "auto" {
			return true
		}
		if strings.Contains(preferred, "_") {
			return preferred == strings.ToLower(codec)
		}
		switch codec {
		case "av1_nvenc", "av1_qsv", "av1_amf", "libsvtav1":
			return wants("av1")
		case "hevc_nvenc", "hevc_qsv", "hevc_amf", "libx265":
			return wants("hevc")
		default:
			return wants("h264")
		}
	}

	hasCUDA := strings.Contains(hwaccelsOut, "cuda")
	hasQSVAccel := strings.Contains(hwaccelsOut, "qsv")

	backend := mediaBackend{}

	setNVENC := func(codec string) mediaBackend {
		b := mediaBackend{
			name:        "nvidia (nvenc " + codec + ")",
			encoderArgs: []string{"-c:v", codec, "-preset", "p5", "-rc", "vbr", "-b:v", "0"},
		}
		switch codec {
		case "av1_nvenc":
			b.encoderArgs = append(b.encoderArgs, "-cq", "28")
		case "hevc_nvenc":
			b.encoderArgs = append(b.encoderArgs, "-cq", "21")
		default:
			b.encoderArgs = append(b.encoderArgs, "-cq", "19")
		}
		if hasCUDA {
			b.decoderArgs = []string{"-hwaccel", "cuda"}
			b.name = "nvidia (cuda+" + codec + ")"
		}
		return b
	}

	setQSV := func(codec string) mediaBackend {
		b := mediaBackend{
			name:        "intel (qsv " + codec + ")",
			encoderArgs: []string{"-c:v", codec, "-global_quality", "23"},
		}
		if codec == "av1_qsv" {
			b.encoderArgs = []string{"-c:v", codec, "-global_quality", "26"}
		}
		if hasQSVAccel {
			b.decoderArgs = []string{"-hwaccel", "qsv"}
			b.name = "intel (qsv decode+" + codec + ")"
		}
		return b
	}

	setAMF := func(codec string) mediaBackend {
		b := mediaBackend{name: "amd (amf " + codec + ")", encoderArgs: []string{"-c:v", codec}}
		if codec != "av1_amf" {
			b.encoderArgs = append(b.encoderArgs, "-quality", "quality")
		}
		return b
	}

	if vendor == "nvidia" {
		switch {
		case hasEncoder("av1_nvenc") && matchesExact("av1_nvenc"):
			backend = setNVENC("av1_nvenc")
		case hasEncoder("hevc_nvenc") && matchesExact("hevc_nvenc"):
			backend = setNVENC("hevc_nvenc")
		case hasEncoder("h264_nvenc") && matchesExact("h264_nvenc"):
			backend = setNVENC("h264_nvenc")
		}
	}

	if backend.name == "" && vendor == "intel" {
		switch {
		case hasEncoder("av1_qsv") && matchesExact("av1_qsv"):
			backend = setQSV("av1_qsv")
		case hasEncoder("hevc_qsv") && matchesExact("hevc_qsv"):
			backend = setQSV("hevc_qsv")
		case hasEncoder("h264_qsv") && matchesExact("h264_qsv"):
			backend = setQSV("h264_qsv")
		}
	}

	if backend.name == "" && vendor == "amd" {
		switch {
		case hasEncoder("av1_amf") && matchesExact("av1_amf"):
			backend = setAMF("av1_amf")
		case hasEncoder("hevc_amf") && matchesExact("hevc_amf"):
			backend = setAMF("hevc_amf")
		case hasEncoder("h264_amf") && matchesExact("h264_amf"):
			backend = setAMF("h264_amf")
		}
	}

	if backend.name == "" {
		switch {
		case hasEncoder("av1_nvenc") && matchesExact("av1_nvenc"):
			backend = setNVENC("av1_nvenc")
		case hasEncoder("av1_qsv") && matchesExact("av1_qsv"):
			backend = setQSV("av1_qsv")
		case hasEncoder("av1_amf") && matchesExact("av1_amf"):
			backend = setAMF("av1_amf")
		case hasEncoder("hevc_nvenc") && matchesExact("hevc_nvenc"):
			backend = setNVENC("hevc_nvenc")
		case hasEncoder("hevc_qsv") && matchesExact("hevc_qsv"):
			backend = setQSV("hevc_qsv")
		case hasEncoder("hevc_amf") && matchesExact("hevc_amf"):
			backend = setAMF("hevc_amf")
		case hasEncoder("h264_nvenc") && matchesExact("h264_nvenc"):
			backend = setNVENC("h264_nvenc")
		case hasEncoder("h264_qsv") && matchesExact("h264_qsv"):
			backend = setQSV("h264_qsv")
		case hasEncoder("h264_amf") && matchesExact("h264_amf"):
			backend = setAMF("h264_amf")
		}
	}

	if backend.name != "" {
		return backend
	}

	if hasEncoder("libsvtav1") && matchesExact("libsvtav1") {
		return mediaBackend{
			name:        "cpu (libsvtav1)",
			encoderArgs: []string{"-c:v", "libsvtav1", "-crf", "32", "-preset", "8"},
		}
	}

	if hasEncoder("libx265") && matchesExact("libx265") {
		return mediaBackend{
			name:        "cpu (libx265)",
			encoderArgs: []string{"-c:v", "libx265", "-crf", "22", "-preset", "medium"},
		}
	}

	return mediaBackend{
		name:        "cpu (libx264)",
		encoderArgs: []string{"-c:v", "libx264", "-crf", "18", "-preset", "slow"},
	}
}

func (a *Anime4KVideoAdapter) ffmpegOutput(ctx context.Context, ffmpeg, commandArg string) string {
	cmd := exec.CommandContext(ctx, ffmpeg, commandArg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func detectGPUVendor() string {
	if runtime.GOOS != "linux" {
		return "unknown"
	}

	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return "nvidia"
	}

	vendorFiles, _ := filepath.Glob("/sys/class/drm/card*/device/vendor")
	hasIntel := false
	hasAMD := false
	hasNVIDIA := false

	for _, f := range vendorFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(strings.ToLower(string(b)))
		switch s {
		case "0x10de":
			hasNVIDIA = true
		case "0x8086":
			hasIntel = true
		case "0x1002", "0x1022":
			hasAMD = true
		}
	}

	if hasNVIDIA {
		return "nvidia"
	}
	if hasIntel {
		return "intel"
	}
	if hasAMD {
		return "amd"
	}
	return "unknown"
}

func (a *Anime4KVideoAdapter) probeFrameCount(ctx context.Context, ffmpeg, input string) (int64, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-i", input)
	out, _ := cmd.CombinedOutput()
	s := string(out)

	var fps float64
	for _, pat := range []string{`(\d+(?:\.\d+)?) fps`, `(\d+(?:\.\d+)?) tbr`} {
		re := regexp.MustCompile(pat)
		if m := re.FindStringSubmatch(s); len(m) > 1 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
				fps = v
				break
			}
		}
	}

	durRe := regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
	var duration float64
	if m := durRe.FindStringSubmatch(s); len(m) > 3 {
		h, _ := strconv.ParseFloat(m[1], 64)
		min, _ := strconv.ParseFloat(m[2], 64)
		sec, _ := strconv.ParseFloat(m[3], 64)
		duration = h*3600 + min*60 + sec
	}

	if fps > 0 && duration > 0 {
		return int64(fps * duration), nil
	}
	return 0, fmt.Errorf("could not determine frame count from ffmpeg probe output")
}
