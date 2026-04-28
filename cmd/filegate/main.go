package main

import (
	"log"
	"os"

	"github.com/valentinkolb/filegate/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}
