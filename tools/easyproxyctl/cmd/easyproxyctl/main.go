package main

import (
	"os"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
