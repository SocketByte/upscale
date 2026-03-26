package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func toolPlatformDir() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	default:
		return "linux"
	}
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func firstExistingPath(candidates []string) (string, bool) {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func firstExistingDir(candidates []string) (string, bool) {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func resolveFFmpegBinary(toolsDir string) string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	bin := exeName("ffmpeg")
	candidates := []string{
		filepath.Join(toolsDir, "ffmpeg", toolPlatformDir(), bin),
		filepath.Join(toolsDir, "ffmpeg", "bin", toolPlatformDir(), bin),
		filepath.Join(toolsDir, "ffmpeg", "bin", bin),
		filepath.Join(toolsDir, "ffmpeg", bin),
	}
	if p, ok := firstExistingPath(candidates); ok {
		return p
	}
	return filepath.Join(toolsDir, "ffmpeg", bin)
}

func resolveRealESRGANBinary(toolsDir string) string {
	if p, err := exec.LookPath("realesrgan-ncnn-vulkan"); err == nil {
		return p
	}
	bin := exeName("realesrgan-ncnn-vulkan")
	candidates := []string{
		filepath.Join(toolsDir, "realesrgan", toolPlatformDir(), bin),
		filepath.Join(toolsDir, "realesrgan", "bin", toolPlatformDir(), bin),
		filepath.Join(toolsDir, "realesrgan", "bin", bin),
		filepath.Join(toolsDir, "realesrgan", bin),
	}
	if p, ok := firstExistingPath(candidates); ok {
		return p
	}
	return filepath.Join(toolsDir, "realesrgan", toolPlatformDir(), bin)
}

func resolveRealESRGANModelsDir(toolsDir string) string {
	candidates := []string{
		filepath.Join(toolsDir, "realesrgan", toolPlatformDir(), "models"),
		filepath.Join(toolsDir, "realesrgan", "models"),
	}
	if p, ok := firstExistingDir(candidates); ok {
		return p
	}
	return filepath.Join(toolsDir, "realesrgan", toolPlatformDir(), "models")
}

func resolveSwinIRDir(toolsDir string) string {
	candidates := []string{
		filepath.Join(toolsDir, "pytorch", "SwinIR"),
		filepath.Join(toolsDir, "SwinIR"),
	}
	if p, ok := firstExistingDir(candidates); ok {
		return p
	}
	return filepath.Join(toolsDir, "pytorch", "SwinIR")
}

func resolveSwinIRScript(toolsDir string) string {
	root := resolveSwinIRDir(toolsDir)
	candidates := []string{
		filepath.Join(root, "main_test_swinir.py"),
		filepath.Join(root, "predict.py"),
	}
	if p, ok := firstExistingPath(candidates); ok {
		return p
	}
	return filepath.Join(root, "main_test_swinir.py")
}

func resolveSwinIRPython(toolsDir string) string {
	for _, name := range []string{"python", "python3", "py"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	if p, ok := resolveSwinIRVenvPython(toolsDir); ok {
		return p
	}

	return "python"
}

func resolveSwinIRVenvPython(toolsDir string) (string, bool) {
	root := resolveSwinIRDir(toolsDir)
	candidates := []string{
		filepath.Join(root, "venv", "Scripts", "python.exe"),
		filepath.Join(root, "venv", "bin", "python"),
	}
	if p, ok := firstExistingPath(candidates); ok {
		return p, true
	}
	return "", false
}

func resolveSwinIRMediumModel(toolsDir string) string {
	root := resolveSwinIRDir(toolsDir)
	return filepath.Join(root, "model_zoo", "swinir", "003_realSR_BSRGAN_DFO_s64w8_SwinIR-M_x4_GAN.pth")
}

func resolveSwinIRLargeModel(toolsDir string) string {
	root := resolveSwinIRDir(toolsDir)
	return filepath.Join(root, "model_zoo", "swinir", "003_realSR_BSRGAN_DFOWMFC_s64w8_SwinIR-L_x4_GAN.pth")
}
