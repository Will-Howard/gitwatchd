package main

import "os"

func main() {
	os.Exit(cliRun(os.Args[1:]))
}
