package main

import "context"

type Adapter interface {
	Name() string
	Prepare(params UpscaleParams) map[string]any
	Run(ctx context.Context, job *Job) error
	Capabilities() Capabilities
}
