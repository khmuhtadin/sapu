package main

import (
	"os"

	"github.com/khmuhtadin/sapu/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
