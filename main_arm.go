package main

import (
	"log"

	ink "github.com/dennwc/inkview"
)

func main() {
	if err := ink.Run(newApp()); err != nil {
		log.Fatalf("pocketbeam: %v", err)
	}
}
