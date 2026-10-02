package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func runServe(args []string) error {
	flags := flag.NewFlagSet("wiregram serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	port := flags.Int("port", 7800, "HTTP port")
	insecure := flags.Bool("insecure", false, "store credentials in plain JSON instead of the OS keyring")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usageText)
			return nil
		}
		return fmt.Errorf("parse serve arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("serve does not accept positional arguments")
	}

	var creds credStore
	if *insecure {
		creds = &insecureStore{}
	} else {
		kr, err := newOSKeyring()
		if err != nil {
			return err
		}
		creds = kr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	headlessPrompt := func(label string, masked bool) (string, error) {
		return "", fmt.Errorf("headless mode cannot prompt for input; configure credentials first via the TUI")
	}
	cfg, dir, err := loadConfig(headlessPrompt, io.Discard)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	lockPath := filepath.Join(dir, "wiregram.lock")
	if err := acquireLock(lockPath); err != nil {
		return err
	}
	defer releaseLock(lockPath)

	mgr, err := loadAccounts(cfg, dir, creds)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	defer mgr.close()

	for _, a := range mgr.accounts {
		mgr.start(ctx, a, nil)
	}

	db, err := openAgentDB(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wiregram: agents disabled — %s\n", cleanError(err))
	} else {
		mgr.agents = newAgentRuntime(ctx, db, mgr)
		mgr.agents.start()
		defer mgr.agents.stop()
	}

	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return fmt.Errorf("generate auth token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])

	fmt.Fprintf(os.Stderr, "wiregram: http://127.0.0.1:%d/?token=%s\n", *port, token)

	return startWeb(ctx, mgr, *port, token)
}

func acquireLock(path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					return fmt.Errorf("another wiregram instance is running (pid %d)", pid)
				}
			}
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600)
}

func releaseLock(path string) {
	os.Remove(path)
}
