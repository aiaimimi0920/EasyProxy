//go:build !windows

package main

import "easy_proxies/internal/config"

func isPlatformService() (bool, error) {
	return false, nil
}

func runPlatformService(_ *config.Config) error {
	return nil
}
