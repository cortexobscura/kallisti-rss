package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/cortexobscura/kallisti-rss/internal/bot"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOfflinePause(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	cfg := filepath.Join(dir, "config.yaml")
	// Invalid DNS destination and missing secret: successful offline operation cannot login.
	err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: https://unused.invalid\norigin: https://unused.invalid\nusername: fixture\npassword_file: %s\ndatabase: %s\nchannels: [5, 7]\n", filepath.Join(dir, "absent-secret"), db)), 0600)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) error {
		oldArgs, oldFlags := os.Args, flag.CommandLine
		defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
		os.Args = append([]string{"feedbot", "-config", cfg}, args...)
		flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return run(ctx)
	}
	s, err := bot.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	seeds := []bot.Seed{{Channel: 5, URL: "https://example.com/old"}, {Channel: 7, URL: "https://example.com/new"}}
	if err = s.Seed(seeds); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO seen(sub,item) VALUES(1,'fixture'); INSERT INTO outbox(channel,nonce,payload,first) VALUES(5,'fixture-nonce','{"text":"fixture"}',123);`); err != nil {
		t.Fatal(err)
	}
	before, err := s.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if err = invoke("-pause-subscription", "1", "-expected-channel", "5"); err == nil {
		t.Fatal("lock bypassed")
	}
	s.Close()
	for _, args := range [][]string{
		{"-pause-subscription", "1"}, {"-expected-channel", "5"},
		{"-pause-subscription", "0", "-expected-channel", "5"},
		{"-pause-subscription", "1", "-expected-channel", "7"},
		{"-pause-subscription", "999", "-expected-channel", "5"},
		{"-pause-subscription", "1", "-expected-channel", "5", "-status"},
		{"-pause-subscription", "1", "-expected-channel", "5", "-discover=false"},
		{"-pause-subscription", "1", "-expected-channel", "5", "unexpected"},
	} {
		if err = invoke(args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	s, err = bot.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	subs, err := s.Subscriptions()
	if err != nil || subs[0].Paused || subs[1].Paused {
		t.Fatalf("failed guard changed state: %+v %v", subs, err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		if err = invoke("-pause-subscription", "1", "-expected-channel", "5"); err != nil {
			t.Fatal(err)
		}
	}
	s, err = bot.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Seed(seeds); err != nil {
		t.Fatal(err)
	}
	subs, err = s.Subscriptions()
	if err != nil || !subs[0].Paused || subs[1].Paused {
		t.Fatalf("pause/reseed: %+v %v", subs, err)
	}
	after, err := s.Inspect()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("queue changed: %v", err)
	}
	var seen int
	if err = s.DB.QueryRow("SELECT count(*) FROM seen").Scan(&seen); err != nil || seen != 1 {
		t.Fatal("seen history changed", err)
	}
}
