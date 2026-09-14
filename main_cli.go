// The plain CLI entry point for every target that is not the PocketBook
// (GOARCH=arm). Keeping the constraint explicit instead of relying on a
// _amd64 filename suffix lets the package build on arm64 and macOS hosts
// too, so the core logic can be run and tested wherever Go runs.

//go:build !arm

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	cfgPath := flag.String("config", "/mnt/ext1/system/config/pocketbeam.cfg", "path to config file")
	verbose := flag.Bool("v", false, "verbose: log every book considered")
	yes := flag.Bool("yes", false, "skip delete-missing confirmation prompt (scripted use)")
	shots := flag.String("screenshots", "", "render the device screens to PNG in this directory and exit (development tool)")
	flag.Parse()

	// The screenshot renderer needs no server and no config: it paints
	// the screens with fixture state. Handled before anything else so
	// `make screenshots` runs on a machine that has neither.
	if *shots != "" {
		if err := writeScreenshots(*shots); err != nil {
			die("screenshots: %v", err)
		}
		return
	}

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
		Profile:       cfg.Profile,
		LibraryShared: LibraryShared(*cfgPath, cfg.Profile, cfg.Library),
	}
	if cfg.DeleteMissing {
		opts.Confirm = cliConfirm(*yes)
	}

	// Translate the first SIGINT / SIGTERM into a context cancel so a
	// Ctrl+C mid-sync stops cleanly (the partial file is left as a .part
	// for the next run's stale-sweep). A second signal falls through to
	// the default handler and kills the process.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ncancelling sync; next signal will terminate")
		cancel()
		signal.Stop(sigCh)
	}()

	res := Sync(ctx, src, store, cfg.Library, progress, opts)
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
