package gitstate

import "testing"

func TestParseSubmoduleStatus(t *testing.T) {
	status := " a38278046b9401fed5bf6205ed41d3ec588cfac4 upstreams/aggregator (heads/main)\n" +
		"-1244581385d50ca600524e89af0b3fdde67918e6 upstreams/ech-workers\n" +
		"+90c2a9ba752ba662290e6838a903603f0d304065 upstreams/misub (heads/main)\n"
	got, err := parseSubmoduleStatus(status)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got["upstreams/ech-workers"] != "1244581385d50ca600524e89af0b3fdde67918e6" {
		t.Fatalf("parseSubmoduleStatus() = %#v", got)
	}
}

func TestParseSubmoduleStatusRejectsDuplicatePath(t *testing.T) {
	_, err := parseSubmoduleStatus(" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa same\n bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb same\n")
	if err == nil {
		t.Fatal("parseSubmoduleStatus() accepted duplicate path")
	}
}
