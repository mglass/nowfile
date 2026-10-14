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

	"github.com/mglass/nowfile/internal/admin"
	"github.com/mglass/nowfile/internal/config"
	"github.com/mglass/nowfile/internal/finger"
	"github.com/mglass/nowfile/internal/mail"
	"github.com/mglass/nowfile/internal/sources"
	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/subs"
	"github.com/mglass/nowfile/internal/web"
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
		return admin.Run(ctx, st, args[1:], os.Stdout)
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
	start(func(ctx context.Context) error {
		return finger.ListenAndServe(ctx, cfg.FingerAddr, finger.FromStore(st, cfg.BaseURL))
	})
	// web.Serve mounts the write API (api.Register) on its mux.
	start(func(ctx context.Context) error { return web.Serve(ctx, cfg, st) })
	// The subscription notifier mails headline changes and prunes the
	// changes history on start and daily.
	start(subs.New(st, mail.New(cfg.PostmarkToken, cfg.MailFrom), cfg.BaseURL).Run)
	// The calendar runner fetches enabled calendar sources and keeps each
	// user's Calendar slot current.
	start(sources.NewRunner(st, sources.NewFetcher(cfg.FetchPrivate)).Run)

	<-ctx.Done()
	wg.Wait()
	log.Printf("nowfile: stopped")
	return errors.Join(errs...)
}
