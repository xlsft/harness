package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/plugins"
)

// noTerminal is why approve is refused without a terminal: the model's shell commands have none, and it must not approve its own
// plugins. Everything else a command does runs nothing.
const noTerminal = "approving a plugin needs someone at a terminal; run this in one, or approve it in Extensions > Plugins"

// runPlugin implements `harness-client plugin ...`.
func runPlugin(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cwd, _ := os.Getwd()
	place := plugins.Place{Cwd: cwd, Trusted: permissions.IsTrusted(cwd)}
	store := bootstrap.PluginStore()
	c := plugins.Caller{Place: place, Prefix: "harness-client plugin"}
	if len(args) > 0 && args[0] == "approve" {
		if !isTerminal(os.Stdin) {
			c.NoApprove = noTerminal
		} else if len(args) == 2 && !confirmApproval(ctx, store, args[1], place) {
			fmt.Fprintln(os.Stderr, "Not approved.")
			return 1
		}
	}
	text, err := plugins.RunCommand(ctx, store, args, c)
	if err != nil {
		return fail(err)
	}
	fmt.Println(text)
	return 0
}

// confirmApproval shows what would run and asks.
func confirmApproval(ctx context.Context, store *plugins.Manager, id string, place plugins.Place) bool {
	text, err := plugins.RunCommand(ctx, store, []string{"info", id}, plugins.Caller{Place: place, Prefix: "harness-client plugin"})
	if err != nil {
		return true // RunCommand says what's wrong with it
	}
	fmt.Println(text)
	fmt.Print("Let this run on this device, with your rights? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}
