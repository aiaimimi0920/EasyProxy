package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"easy_proxies/internal/app"
	"easy_proxies/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()
	serviceMode, err := isPlatformService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to detect platform service mode: %v\n", err)
		os.Exit(1)
	}
	if serviceMode && !filepath.IsAbs(*configPath) {
		fmt.Fprintln(os.Stderr, "Windows Service mode requires an absolute -config path")
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if serviceMode {
		err = runPlatformService(cfg)
	} else {
		err = app.Run(context.Background(), cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
