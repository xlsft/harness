// Command fakegateway runs the scripted fake harness server for local
// end-to-end runs of harness-client.
//
//	go run ./internal/fakegateway/cmd/fakegateway -addr 127.0.0.1:7777 -scenario fix-test
//	go run ./internal/fakegateway/cmd/fakegateway -init-project /tmp/demo   # writes the demo project
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/internal/fakegateway"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "listen address")
	scenario := flag.String("scenario", "fix-test", "fix-test | echo")
	initProject := flag.String("init-project", "", "write the demo project into this directory and exit")
	flag.Parse()
	if *initProject != "" {
		for name, content := range fakegateway.DemoFiles {
			p := filepath.Join(*initProject, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		return
	}
	var r fakegateway.Responder
	switch *scenario {
	case "fix-test":
		r = fakegateway.FixTestDemo()
	case "echo":
		r = nil
	default:
		fmt.Fprintln(os.Stderr, "unknown scenario", *scenario)
		os.Exit(2)
	}
	s := fakegateway.New(r)
	url, stop, err := s.Listen(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer stop()
	fmt.Println("fake harness server listening on", url)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
