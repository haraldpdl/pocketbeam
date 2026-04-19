package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	cfgPath := flag.String("config", "/mnt/ext1/system/config/pocketbeam.cfg", "path to config file")
	verbose := flag.Bool("v", false, "verbose: log every book considered")
	yes := flag.Bool("yes", false, "skip delete-missing confirmation prompt (scripted use)")
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

	opts := SyncOptions{
		DeleteMissing: cfg.DeleteMissing,
		Scope:         ScopeFor(cfg),
	}
	if cfg.DeleteMissing {
		opts.Confirm = cliConfirm(*yes)
	}

	res := Sync(src, store, cfg.Library, progress, opts)
	fmt.Printf("done: %d downloaded, %d skipped, %d failed, %d deleted\n",
		res.Downloaded, res.Skipped, res.Failed, res.Deleted)
	if res.FirstErr != nil {
		fmt.Fprintf(os.Stderr, "first error: %v\n", res.FirstErr)
		os.Exit(1)
	}
}

// cliConfirm returns a Confirm that prints the deletion set and reads a
// yes/no answer from stdin. When skip is true the answer is assumed yes,
// for scripted or cron-driven use.
func cliConfirm(skip bool) Confirm {
	return func(deletions []LocalBook) bool {
		fmt.Printf("\n%d book(s) no longer on the remote:\n", len(deletions))
		const preview = 10
		for i, d := range deletions {
			if i == preview {
				fmt.Printf("  ... and %d more\n", len(deletions)-preview)
				break
			}
			fmt.Printf("  - %s - %s\n", d.Author, d.Title)
		}
		if skip {
			fmt.Println("--yes given; deleting without prompt")
			return true
		}
		fmt.Print("Delete these from the device? [y/N] ")
		r := bufio.NewReader(os.Stdin)
		ans, _ := r.ReadString('\n')
		return strings.EqualFold(strings.TrimSpace(ans), "y")
	}
}
