package main

import (
	"context"
	"strconv"
	"strings"
)

const (
	QualityLow    = 1
	QualityMedium = 2
	QualityHigh   = 3
	QualityUltra  = 4
)

type Modifier string

const (
	ModifierNormal Modifier = "normal"
	ModifierAnime  Modifier = "anime"
)

type UpscaleParams struct {
	Scale         int
	Quality       int
	Modifier      Modifier
	AdapterParams map[string]any
}

func adapterParamString(params UpscaleParams, keys ...string) (string, bool) {
	if params.AdapterParams == nil {
		return "", false
	}
	for _, key := range keys {
		v, ok := params.AdapterParams[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return "", false
		}
		return s, true
	}
	return "", false
}

func adapterParamInt(params UpscaleParams, keys ...string) (int, bool) {
	if params.AdapterParams == nil {
		return 0, false
	}
	for _, key := range keys {
		v, ok := params.AdapterParams[key]
		if !ok {
			continue
		}
		switch value := v.(type) {
		case float64:
			return int(value), true
		case int:
			return value, true
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return 0, false
			}
			return parsed, true
		default:
			return 0, false
		}
	}
	return 0, false
}

func adapterParamBool(params UpscaleParams, keys ...string) (bool, bool) {
	if params.AdapterParams == nil {
		return false, false
	}
	for _, key := range keys {
		v, ok := params.AdapterParams[key]
		if !ok {
			continue
		}
		switch value := v.(type) {
		case bool:
			return value, true
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return false, false
			}
			return parsed, true
		default:
			return false, false
		}
	}
	return false, false
}

type Capabilities struct {
	SupportsDenoise bool
	SupportsPrompt  bool
	SupportsFaces   bool
}

type Pipeline struct {
	adapter Adapter
}

func NewPipeline(adapter Adapter) *Pipeline {
	return &Pipeline{adapter: adapter}
}
func (p *Pipeline) Run(ctx context.Context, input, output string, params UpscaleParams) *Job {
	job := &Job{
		InputPath:  input,
		OutputPath: output,
		Params:     params,
		Events:     make(chan Event, 64),
	}

	go func() {
		defer close(job.Events)
		if err := p.adapter.Run(ctx, job); err != nil {
			job.Events <- Event{Type: EventError, Message: err.Error()}
		}
	}()

	return job
}
