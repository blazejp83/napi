//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "napiw is the Windows menu launcher; use napi on this system")
	os.Exit(1)
}
