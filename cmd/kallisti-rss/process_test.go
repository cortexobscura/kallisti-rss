package main

import (
	"context"
	"fmt"
	"github.com/kallisti-chat/kallisti-rss/internal/bot"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestProcessSignalAndLockRelease(t *testing.T) {
	if os.Getenv("KALLISTI_TEST_PROCESS") == "1" {
		os.Args = []string{"kallisti-rss", "-config", os.Getenv("KALLISTI_TEST_CONFIG")}
		main()
		return
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	config := filepath.Join(dir, "config.yaml")
	// Missing token makes reconnect fail before any networking; signal during retry wait.
	content := fmt.Sprintf("server: https://unused.invalid\norigin: https://unused.invalid\nusername: fixture\ntoken_file: %s\ndatabase: %s\nchannels: [1]\n", filepath.Join(dir, "absent"), db)
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessSignalAndLockRelease$")
	cmd.Env = append(os.Environ(), "KALLISTI_TEST_PROCESS=1", "KALLISTI_TEST_CONFIG="+config)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	locked := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(db); err == nil {
			s, e := bot.OpenStore(db)
			if e != nil {
				locked = true
				break
			}
			s.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !locked {
		t.Fatal("child did not acquire lock")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal("unclean signal exit", err)
	}
	s, err := bot.OpenStore(db)
	if err != nil {
		t.Fatal("lock leaked", err)
	}
	defer s.Close()
	if _, err = s.Status(); err != nil {
		t.Fatal(err)
	}
}
