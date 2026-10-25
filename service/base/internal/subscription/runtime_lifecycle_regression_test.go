package subscription

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

type completionBarrierContext struct {
	context.Context
	completed <-chan struct{}
}

func (c completionBarrierContext) Deadline() (time.Time, bool) {
	// Force the refresh to finish before WithTimeout returns to RefreshNow.
	select {
	case <-c.completed:
	case <-c.Context.Done():
	}
	return c.Context.Deadline()
}

func TestRefreshNowObservesCompletionBeforeWaitStarts(t *testing.T) {
	for _, refreshError := range []string{"", "synthetic fetch failure"} {
		t.Run(fmt.Sprintf("error=%q", refreshError), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			completed := make(chan struct{})
			m := &Manager{
				baseCfg:       &config.Config{Subscriptions: []string{"https://example.invalid/sub"}},
				ctx:           completionBarrierContext{Context: ctx, completed: completed},
				manualRefresh: make(chan struct{}, 1),
				refreshDone:   make(chan struct{}),
			}
			go func() {
				select {
				case <-m.manualRefresh:
					m.mu.Lock()
					m.status.LastError = refreshError
					m.status.RefreshCount++
					close(m.refreshDone)
					m.refreshDone = make(chan struct{})
					m.mu.Unlock()
				case <-ctx.Done():
				}
				close(completed)
			}()
			err := m.RefreshNow()
			if refreshError == "" && err != nil {
				t.Fatalf("completed refresh returned %v", err)
			}
			if refreshError != "" && (err == nil || !strings.Contains(err.Error(), refreshError)) {
				t.Fatalf("completed refresh error = %v, want %q", err, refreshError)
			}
		})
	}
}

type capturedConnectorLogger struct{ lines []string }

func (l *capturedConnectorLogger) Infof(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}
func (l *capturedConnectorLogger) Warnf(format string, args ...any)  { l.Infof(format, args...) }
func (l *capturedConnectorLogger) Errorf(format string, args ...any) { l.Infof(format, args...) }

func TestConnectorLogsDrainOversizedLineAndContinue(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	logger := &capturedConnectorLogger{}
	m := &connectorRuntimeManager{logger: logger}
	writeDone := make(chan error, 1)
	readDone := make(chan struct{})
	go func() {
		_, err := io.WriteString(writer, strings.Repeat("x", 256*1024)+"\nafter-long-line\nfinal-no-newline")
		writer.Close()
		writeDone <- err
	}()
	go func() {
		m.pipeLogs("test", "stdout", reader)
		close(readDone)
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connector output stopped draining after an oversized line")
	}
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("log reader did not finish")
	}
	output := strings.Join(logger.lines, "\n")
	if !strings.Contains(output, "after-long-line") || !strings.Contains(output, "final-no-newline") {
		t.Fatal("normal lines after oversized output were lost")
	}
	if !strings.Contains(output, "truncated") || len(output) > 66*1024 {
		t.Fatal("oversized line was not reported with bounded output")
	}
}
