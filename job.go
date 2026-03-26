package main

type Job struct {
	InputPath  string
	OutputPath string
	Params     UpscaleParams
	Events     chan Event
}
