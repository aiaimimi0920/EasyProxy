package tuncheck

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tun "github.com/sagernet/sing-tun"
)

func TestTUNCloseReleasesInterfaceWithLiveChild(t *testing.T) {
	if os.Getenv("EASYPROXY_TEST_TUN") != "1" {
		t.Skip("requires an isolated network namespace and CAP_NET_ADMIN")
	}
	options := tun.Options{Name: "ep-fd-test", MTU: 1500}
	device, err := tun.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()

	child := exec.Command(os.Args[0], "-test.run=^TestTUNDescriptorChild$")
	child.Env = append(os.Environ(), "EASYPROXY_TUN_CHILD=1")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatalf("child readiness: %v", err)
	}
	if line != "inherited=0\n" {
		t.Errorf("child retained a TUN descriptor: %s", line)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	// Keep the child alive while reopening the same interface, as on reload.
	replacement, err := tun.New(options)
	if err != nil {
		t.Fatalf("reopen with live child: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTUNDescriptorChild(t *testing.T) {
	if os.Getenv("EASYPROXY_TUN_CHILD") != "1" {
		return
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	inherited := 0
	for _, entry := range entries {
		target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if target == "/dev/net/tun" {
			inherited++
		}
	}
	fmt.Printf("inherited=%d\n", inherited)
	var signal [1]byte
	_, _ = os.Stdin.Read(signal[:])
}
