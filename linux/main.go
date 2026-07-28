package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gitwatchd: not implemented on Linux yet")
	os.Exit(1)
}
