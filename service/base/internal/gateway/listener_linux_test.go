//go:build linux

package gateway

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestConfigureTransparentSocketSetsOnlyRequiredOptions(t *testing.T) {
	type call struct {
		level  int
		option int
		value  int
	}
	var calls []call
	setOption := func(_ int, level, option, value int) error {
		calls = append(calls, call{level: level, option: option, value: value})
		return nil
	}

	if err := configureTransparentSocket(42, setOption); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{level: unix.SOL_SOCKET, option: unix.SO_REUSEADDR, value: 1},
		{level: unix.IPPROTO_IP, option: unix.IP_TRANSPARENT, value: 1},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("socket options = %#v, want %#v", calls, want)
	}
}
