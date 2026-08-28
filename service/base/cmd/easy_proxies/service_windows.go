//go:build windows

package main

import (
	"context"
	"fmt"

	"easy_proxies/internal/app"
	"easy_proxies/internal/config"
	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "EasyProxy"

type windowsService struct {
	config *config.Config
}

func isPlatformService() (bool, error) {
	return svc.IsWindowsService()
}

func runPlatformService(cfg *config.Config) error {
	if err := svc.Run(windowsServiceName, &windowsService{config: cfg}); err != nil {
		return fmt.Errorf("run Windows Service: %w", err)
	}
	return nil
}

func (service *windowsService) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- app.RunWithContext(ctx, service.config) }()

	status := svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	changes <- status
	for {
		select {
		case err := <-done:
			if err != nil {
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- status
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				if err := <-done; err != nil {
					return false, 1
				}
				return false, 0
			}
		}
	}
}
