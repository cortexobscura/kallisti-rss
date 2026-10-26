package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cortexobscura/kallisti-rss/internal/bot"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	path := flag.String("config", "config.yaml", "YAML configuration path")
	demo := flag.Bool("demo", false, "run credential-free offline fixture demo")
	check := flag.Bool("check-config", false, "validate configuration without networking")
	status := flag.Bool("status", false, "inspect durable queue without connecting")
	inspect := flag.Bool("outbox", false, "inspect up to 1000 unresolved jobs offline as JSON")
	resolve := flag.Int64("resolve-job", 0, "offline job ID to reconcile manually")
	resolution := flag.String("resolution", "", "delivered or discarded after operator reconciliation")
	discover := flag.Bool("discover", false, "authenticate and print IDs/channels; no polling, posts or database writes")
	checkFeeds := flag.Bool("check-feeds", false, "read-only fetch/parse checks; no login, media or database changes")
	subscriptions := flag.Bool("subscriptions", false, "offline sanitized subscription-to-YAML mapping")
	pause := flag.Int64("pause-subscription", 0, "offline subscription ID to pause without changing queued work")
	expectedChannel := flag.Int64("expected-channel", 0, "required stored channel guard for offline subscription pause")
	flag.Parse()
	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["pause-subscription"] || visited["expected-channel"] {
		if !visited["pause-subscription"] || !visited["expected-channel"] || *pause <= 0 || *expectedChannel <= 0 {
			return fmt.Errorf("pause requires positive -pause-subscription and -expected-channel")
		}
		for name := range visited {
			if name != "config" && name != "pause-subscription" && name != "expected-channel" {
				return fmt.Errorf("offline pause cannot be combined with other action flags")
			}
		}
		if flag.NArg() != 0 {
			return fmt.Errorf("offline pause does not accept positional arguments")
		}
	}
	if *demo {
		return bot.Demo(ctx, os.Stdout)
	}
	var cfg bot.Config
	var e error
	if *discover {
		cfg, e = bot.LoadDiscoveryConfig(*path)
	} else {
		cfg, e = bot.LoadConfig(*path)
	}
	if e != nil {
		return e
	}
	if *check {
		fmt.Println("configuration valid")
		return nil
	}
	if *checkFeeds {
		return bot.CheckFeeds(ctx, cfg, bot.NewFetcher(cfg.AllowHTTP))
	}
	if *discover {
		cfg.Channels = nil
		conn, e := bot.NewDialer(cfg).Connect(ctx)
		if e != nil {
			return e
		}
		defer conn.Close()
		return json.NewEncoder(os.Stdout).Encode(conn.Discovery())
	}
	store, e := bot.OpenStore(cfg.Database)
	if e != nil {
		return e
	}
	defer store.Close()
	if e = store.Bind(strings.TrimRight(cfg.Server, "/") + "|" + cfg.Username); e != nil {
		return e
	}
	if *pause > 0 {
		if e := store.PauseSubscription(*pause, *expectedChannel); e != nil {
			return e
		}
		fmt.Printf("subscription %d paused in channel %d; queued work unchanged\n", *pause, *expectedChannel)
		return nil
	}
	if *subscriptions {
		subs, e := store.Subscriptions()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(bot.SubscriptionViews(cfg, subs))
	}
	if *inspect {
		jobs, e := store.Inspect()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(jobs)
	}
	if *resolve > 0 {
		return store.Resolve(*resolve, *resolution)
	}
	if *status {
		v, e := store.Status()
		fmt.Println(v)
		return e
	}
	if e = store.Seed(cfg.Feeds); e != nil {
		return e
	}
	fetch := bot.NewFetcher(cfg.AllowHTTP)
	dialer := bot.NewDialer(cfg)
	engine := bot.Engine{Config: cfg, Store: store, Fetcher: fetch}
	for ctx.Err() == nil {
		conn, e := dialer.Connect(ctx)
		if e != nil {
			slog.Warn("connection unavailable; retrying in 30s")
		} else {
			if er := store.BindUser(conn.UserID()); er != nil {
				conn.Close()
				return er
			}
			engine.Sender = conn
			poll := time.NewTicker(time.Second)
			drain := time.NewTicker(5 * time.Second)
			if er := engine.PollDue(ctx); er != nil {
				slog.Warn("feed failures recorded; other subscriptions continue and failed feeds retry next poll")
			}
			_ = engine.Drain(ctx) // Delivery logs classify each actual failed attempt.
			connected := true
			for connected {
				select {
				case <-ctx.Done():
					connected = false
				case <-conn.Done():
					connected = false
				case m := <-conn.Commands:
					if er := engine.Command(ctx, m); er != nil {
						slog.Error("command transaction failed")
					}
				case <-poll.C:
					if er := engine.PollDue(ctx); er != nil {
						slog.Warn("feed failures recorded; other subscriptions continue and failed feeds retry next poll")
					}
				case <-drain.C:
					_ = engine.Drain(ctx) // Cooldown ticks do not emit duplicate warnings.
				}
			}
			poll.Stop()
			drain.Stop()
			conn.Close()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx); e != nil {
		slog.Error("feedbot stopped", "error", e)
		os.Exit(1)
	}
}
