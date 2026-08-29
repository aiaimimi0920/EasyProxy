package subscription

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (m *connectorRuntimeManager) startInstance(spec connectorSpec, startupTimeout time.Duration) (*connectorInstance, error) {
	ctx, cancel := context.WithCancel(m.ctx)
	cmd := exec.CommandContext(ctx, spec.BinaryPath, spec.Args...)
	cmd.Dir = spec.WorkingDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start process: %w", err)
	}

	instance := &connectorInstance{
		spec:   spec,
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan error, 1),
	}

	go m.pipeLogs(spec.DisplayName, "stdout", stdout)
	go m.pipeLogs(spec.DisplayName, "stderr", stderr)
	go func() {
		instance.done <- cmd.Wait()
		close(instance.done)
	}()

	if err := waitForConnectorListen(spec.ListenAddr, startupTimeout); err != nil {
		_ = m.stopInstance(instance)
		return nil, err
	}

	m.logger.Infof("started connector %s on %s", spec.DisplayName, spec.ListenAddr)
	return instance, nil
}

func (m *connectorRuntimeManager) stopInstance(instance *connectorInstance) error {
	if instance == nil {
		return nil
	}

	instance.cancel()
	if instance.cmd != nil && instance.cmd.Process != nil {
		_ = instance.cmd.Process.Kill()
	}

	select {
	case err := <-instance.done:
		if err != nil && !errors.Is(err, context.Canceled) && !isKilledProcessError(err) {
			return err
		}
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timeout waiting for connector %s to stop", instance.spec.DisplayName)
	}

	m.logger.Infof("stopped connector %s", instance.spec.DisplayName)
	return nil
}

func (i *connectorInstance) isRunning() bool {
	if i == nil {
		return false
	}
	select {
	case <-i.done:
		return false
	default:
		return true
	}
}

func (m *connectorRuntimeManager) pipeLogs(name string, stream string, reader interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m.logger.Infof("[connector:%s:%s] %s", name, stream, line)
	}
}

func waitForConnectorListen(addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("connector listen timeout on %s", addr)
}

func resolveConnectorBinary(configuredPath string) (string, error) {
	if configuredPath != "" {
		path, err := exec.LookPath(configuredPath)
		if err == nil {
			return path, nil
		}
		if filepath.IsAbs(configuredPath) {
			if _, statErr := os.Stat(configuredPath); statErr == nil {
				return configuredPath, nil
			}
		}
		return "", fmt.Errorf("connector binary %q not found", configuredPath)
	}

	candidates := []string{"ech-workers", "ech-win"}
	if runtime.GOOS == "windows" {
		candidates = []string{"ech-workers.exe", "ech-win.exe", "ech-workers", "ech-win"}
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("ech-workers binary not found in PATH")
}
