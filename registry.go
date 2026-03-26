package main

import "fmt"

type AdapterFactory func(toolsDir string) Adapter

var adapterRegistry = map[string]AdapterFactory{
	"realesrgan": func(toolsDir string) Adapter {
		return NewRealESRGANAdapter(toolsDir)
	},
	"swinir": func(toolsDir string) Adapter {
		return NewSwinIRAdapter(toolsDir)
	},
	"realesrgan-video": func(toolsDir string) Adapter {
		return NewRealESRGANVideoAdapter(toolsDir)
	},
	"anime4k-video": func(toolsDir string) Adapter {
		return NewAnime4KVideoAdapter(toolsDir)
	},
}

func newAdapter(name, toolsDir string) (Adapter, error) {
	factory, ok := adapterRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q", name)
	}
	return factory(toolsDir), nil
}

func adapterNames() []string {
	names := make([]string, 0, len(adapterRegistry))
	for k := range adapterRegistry {
		names = append(names, k)
	}
	return names
}
