// kgread is the read-only reader of a kgai store that editor extensions talk to. It
// finds the store exactly as kg does, replays the decision log in memory
// (internal/view over internal/replay) and answers questions about it — overview,
// decisions with exact filters, elements, contested elements, people — as JSON over
// stdio, one request per line, one response per line, plus a notification whenever
// the log changes on disk. It never opens the graph database, never takes the store's
// lock, never writes, never syncs, never calls the network. Pure Go: it builds for
// every platform an editor runs on, Windows included.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"kgai/internal/view"
)

// version is the release version, injected at build time via -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `kgread — read-only reader of a kgai store, for editor extensions

usage: kgread [--dir DIR] [--watch 2s]              serve requests on stdin, answers on stdout
       kgread [--dir DIR] --once METHOD [PARAMS]     answer one request and exit
       kgread --version

  --dir DIR      resolve the store as kg would when run in DIR (default: the working
                 directory): KGAI_STORE, then an approved .kgairc, then
                 ~/.kgai/config.json, then <project>/.kgai/store.
  --watch D      look for changes to the log every D (0 = never); a change is
                 announced as {"method":"changed","params":<status>}.
  --once M [P]   one of the methods below with its JSON params, printed indented.

Protocol (one JSON object per line):
  request        {"id":1,"method":"decisions","params":{"state":"head"}}
  response       {"id":1,"result":...}  or  {"id":1,"error":"..."}
  methods        status, open{dir}, refresh, overview, filters, decisions{filter},
                 decision{id}, elements{kind,text}, element{id}, conflicts,
                 people{by}, person{name,by}
`)
	}
	dir := flag.String("dir", "", "")
	showVersion := flag.Bool("version", false, "")
	once := flag.String("once", "", "")
	interval := flag.Duration("watch", 2*time.Second, "")
	flag.Parse()
	if *showVersion {
		fmt.Printf("kgread %s\n", version)
		return
	}
	if *once != "" {
		params := json.RawMessage(nil)
		if flag.NArg() > 0 {
			params = json.RawMessage(flag.Arg(0))
		}
		s := newServer(view.Load(*dir), os.Stdout)
		res, err := s.dispatch(*once, params)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kgread:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(os.Stderr, "kgread:", err)
			os.Exit(1)
		}
		return
	}
	if err := serve(os.Stdin, os.Stdout, *dir, *interval); err != nil {
		fmt.Fprintln(os.Stderr, "kgread:", err)
		os.Exit(1)
	}
}
