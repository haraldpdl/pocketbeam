package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	cfgPath := flag.String("config", "/mnt/ext1/system/config/pocketbeam.cfg", "path to config file")
	verbose := flag.Bool("v", false, "verbose: log every book considered")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		die("config: %v", err)
	}
	src, err := newSource(cfg)
	if err != nil {
		die("source: %v", err)
	}
	store, err := OpenStore(cfg.StateDB)
	if err != nil {
		die("store: %v", err)
	}
	defer store.Close()

	fmt.Printf("pocketbeam %s: syncing %s to %s\n", version, cfg.Host, cfg.Library)

	var progress Progress
	if *verbose {
		progress = func(i, total int, b Book) {
			fmt.Printf("[%d/%d] %s: %s\n", i, total, b.Author, b.Title)
		}
	}

	dl, skip, fail, firstErr := Sync(src, store, cfg.Library, progress)
	fmt.Printf("done: %d downloaded, %d skipped, %d failed\n", dl, skip, fail)
	if firstErr != nil {
		fmt.Fprintf(os.Stderr, "first error: %v\n", firstErr)
		os.Exit(1)
	}
}
