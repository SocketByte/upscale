package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/schollz/progressbar/v3"
)

type cliFlags struct {
	input       string
	output      string
	scale       int
	quality     int
	modifier    string
	toolsDir    string
	paramsJSON  string
	adapterName string
	raw         bool
	logLevel    string
}

func bindFlags(fs *flag.FlagSet, f *cliFlags, defaultAdapter string) {
	fs.StringVar(&f.input, "i", "", "input path (required)")
	fs.StringVar(&f.output, "o", "", "output path (required)")
	fs.IntVar(&f.scale, "s", 4, "upscale factor (adapter-specific; typically 2–4)")
	fs.IntVar(&f.quality, "q", QualityHigh, "quality level: 1=low, 2=medium, 3=high, 4=ultra")
	fs.StringVar(&f.modifier, "m", "normal", "content modifier: normal, anime")
	fs.StringVar(&f.adapterName, "a", defaultAdapter, "adapter to use ("+strings.Join(adapterNames(), ", ")+")")
	fs.StringVar(&f.toolsDir, "tdir", "", "tools directory (default: auto-detected)")
	fs.StringVar(&f.paramsJSON, "p", "{}", "adapter-specific JSON params (example: '{\"mode\":\"b\",\"encoder\":\"av1\"}')")
	fs.BoolVar(&f.raw, "r", false, "print raw percentage floats instead of a progress bar (useful for GUI tools using this as a backend)")
	fs.StringVar(&f.logLevel, "log", "debug", "log level: trace, debug, info, warn, error, fatal")
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "image":
		runSubcommand(os.Args[2:], "image", "realesrgan")
	case "video":
		runSubcommand(os.Args[2:], "video", "realesrgan-video")
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: upscale <command> [options]\n\nCommands:\n")
	fmt.Fprintf(os.Stderr, "  image   upscale a single image\n")
	fmt.Fprintf(os.Stderr, "  video   upscale a video file\n")
	fmt.Fprintf(os.Stderr, "\nUse -a to select adapter (example: -a anime4k-video).\n")
	fmt.Fprintf(os.Stderr, "\nRun 'upscale <command> -h' for command-specific options.\n")
}

func runSubcommand(args []string, name, defaultAdapter string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var f cliFlags
	bindFlags(fs, &f, defaultAdapter)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: upscale %s -i <input> -o <output> [options]\n\nOptions:\n", name)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if lvl, err := zerolog.ParseLevel(f.logLevel); err == nil {
		zerolog.SetGlobalLevel(lvl)
	} else {
		log.Fatal().Str("level", f.logLevel).Msg("invalid log level")
	}

	if f.input == "" || f.output == "" {
		fs.Usage()
		os.Exit(1)
	}

	if f.quality < QualityLow || f.quality > QualityUltra {
		log.Fatal().Int("quality", f.quality).Msg("quality must be between 1 and 4")
	}

	mod := Modifier(f.modifier)
	if mod != ModifierNormal && mod != ModifierAnime {
		log.Fatal().Str("modifier", f.modifier).Msg("modifier must be one of: normal, anime")
	}

	if f.toolsDir == "" {
		f.toolsDir = resolveToolsDir()
	}

	adapterParams, err := parseAdapterParams(f.paramsJSON)
	if err != nil {
		log.Fatal().Err(err).Str("params", f.paramsJSON).Msg("invalid -params JSON")
	}

	adapter, err := newAdapter(f.adapterName, f.toolsDir)
	if err != nil {
		log.Fatal().Err(err).Str("available", strings.Join(adapterNames(), ", ")).Msg("unknown adapter")
	}

	params := UpscaleParams{Scale: f.scale, Quality: f.quality, Modifier: mod, AdapterParams: adapterParams}
	pipeline := NewPipeline(adapter)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info().Msg("")
	log.Info().Msg(" _  _  ____  ____   ___   __   __    ____ ")
	log.Info().Msg("/ )( \\(  _ \\/ ___) / __) / _\\ (  )  (  __)")
	log.Info().Msg(") \\/ ( ) __/\\___ \\( (__ /    \\/ (_/\\ ) _) ")
	log.Info().Msg("\\____/(____)(____/ \\___)\\_/\\_/\\____/(____)")
	log.Info().Msg("")

	logPlatformDiagnostics(f.toolsDir)

	log.Info().Str("input", f.input).Str("output", f.output).Str("adapter", adapter.Name()).Msg("upscaling")

	var bar *progressbar.ProgressBar
	if !f.raw {
		bar = progressbar.NewOptions(100,
			progressbar.OptionSetDescription("upscaling"),
			progressbar.OptionSetWidth(40),
			progressbar.OptionShowBytes(false),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "=",
				SaucerHead:    ">",
				SaucerPadding: " ",
				BarStart:      "[",
				BarEnd:        "]",
			}),
			progressbar.OptionClearOnFinish(),
			progressbar.OptionSetWriter(os.Stderr),
		)
	}

	job := pipeline.Run(ctx, f.input, f.output, params)
	exitCode := 0
	lastPct := -1

	for event := range job.Events {
		switch event.Type {
		case EventProgress:
			pct := int(event.Progress * 100)
			if pct == lastPct {
				continue
			}
			lastPct = pct
			if f.raw {
				fmt.Printf("PROGRESS=%f\n", event.Progress*100)
			} else {
				_ = bar.Set(pct)
			}
		case EventLog:
			if !f.raw {
				fmt.Fprintf(os.Stderr, "\r\033[2K")
				log.Trace().Msg(event.Message)
				_ = bar.RenderBlank()
			} else {
				log.Trace().Msg(event.Message)
			}
		case EventError:
			if !f.raw {
				fmt.Fprintf(os.Stderr, "\r\033[2K")
			}
			log.Error().Msg(event.Message)
			exitCode = 1
		}
	}

	if exitCode == 0 {
		if !f.raw {
			_ = bar.Finish()
		}
		log.Info().Str("output", f.output).Msg("done")
	}
	os.Exit(exitCode)
}

func resolveToolsDir() string {
	if dir := os.Getenv("UPSCALE_TOOLS_DIR"); dir != "" {
		return dir
	}
	if _, err := os.Stat("tools"); err == nil {
		return "tools"
	}
	exe, err := os.Executable()
	if err != nil {
		return "tools"
	}
	return filepath.Join(filepath.Dir(exe), "tools")
}

func parseAdapterParams(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	if params == nil {
		return map[string]any{}, nil
	}
	return params, nil
}

func logPlatformDiagnostics(toolsDir string) {
	log.Debug().Str("goos", runtime.GOOS).Str("goarch", runtime.GOARCH).Str("platform_dir", toolPlatformDir()).Msg("runtime")

	ffmpegBundled := resolveFFmpegBinary(toolsDir)
	realesrganBundled := resolveRealESRGANBinary(toolsDir)

	log.Debug().Str("path", ffmpegBundled).Str("state", pathState(ffmpegBundled)).Msg("ffmpeg bundled")
	log.Debug().Str("path", realesrganBundled).Str("state", pathState(realesrganBundled)).Msg("realesrgan binary")
}

func pathState(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	if st.IsDir() {
		return "dir"
	}
	if st.Mode()&0o111 != 0 {
		return "file-executable"
	}
	return "file"
}
