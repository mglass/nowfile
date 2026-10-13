// Command nowfile serves nowfile documents over finger and HTTP from one
// SQLite database. "nowfile admin ..." runs admin commands against the same
// database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mglass/nowfile/internal/config"
	"github.com/mglass/nowfile/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Printf("nowfile: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if len(args) > 0 && args[0] == "admin" {
		// TODO(admin slice): return admin.Run(ctx, st, args[1:])
		return errors.New("admin commands are not built yet")
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q", args[0])
	}

	log.Printf("nowfile: starting: %s", cfg)

	// start runs a listener until ctx is cancelled. A listener returns nil
	// on clean shutdown; an error from any listener stops all of them.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	start := func(serve func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := serve(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
			}
		}()
	}
	_ = start // remove once a listener below is wired

	// Hook points. Each slice replaces its own line.
	// TODO(finger slice): start(func(ctx context.Context) error { return finger.Serve(ctx, cfg.FingerAddr, st) })
	// TODO(web slice): start(func(ctx context.Context) error { return web.Serve(ctx, cfg, st) })

	<-ctx.Done()
	wg.Wait()
	log.Printf("nowfile: stopped")
	return errors.Join(errs...)
}
