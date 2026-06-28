// Command kg is the local CLI for the kgai knowledge graph: a small, stable graph of
// domain ELEMENTS (application & business things) shaped by an append-only, immutable
// log of DECISIONS. Decisions mutate the element graph and carry who/why/when, so the
// full evolution of every element is preserved and queryable.
//
// Output is JSON on stdout (the primary consumer is the AI via the plugin skill).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"kgai/internal/engine"
	"kgai/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if err := dispatch(os.Args[1], os.Args[2:]); err != nil {
		emit(map[string]any{"ok": false, "error": err.Error()})
		os.Exit(1)
	}
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "init":
		return cmdInit(args)
	case "ingest":
		return cmdIngest(args)
	case "resolve":
		return cmdResolve(args)
	case "query":
		return cmdQuery(args)
	case "search":
		return cmdSearch(args)
	case "context":
		return cmdContext(args)
	case "history":
		return cmdHistory(args)
	case "as-of":
		return cmdAsOf(args)
	case "conflicts":
		return cmdConflicts(args)
	case "sync":
		return cmdSync(args)
	case "rebuild":
		return cmdRebuild(args)
	case "export":
		return cmdExport(args)
	case "doctor":
		return cmdDoctor(args)
	case "version", "-v", "--version":
		emit(map[string]any{"ok": true, "name": "kg", "schema_version": store.SchemaVersion})
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func open() (*engine.Engine, error) {
	s, err := store.Open(os.Getenv("KGAI_STORE"))
	if err != nil {
		return nil, err
	}
	return engine.New(s), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	root := fs.String("root", "", "store root (default: $KGAI_STORE or $KGAI_HOME/store or ~/.kgai/store)")
	actor := fs.String("actor", "", "actor/author name for this install")
	remote := fs.String("remote", "", "git remote URL for sync")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := store.Init(*root, *actor, *remote)
	if err != nil {
		return err
	}
	if _, err := engine.New(s).Rebuild(); err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "root": s.Root, "install_id": s.Config.InstallID, "actor": s.Config.Actor, "remote": s.Config.Remote})
	return nil
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	file := fs.String("file", "", "read JSON payload from file instead of stdin")
	dry := fs.Bool("dry-run", false, "resolve and report without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var raw []byte
	var err error
	if *file != "" {
		raw, err = os.ReadFile(*file)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	var in engine.IngestInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid ingest JSON: %w", err)
	}
	e, err := open()
	if err != nil {
		return err
	}
	res, err := e.Ingest(in, *dry)
	if err != nil {
		return err
	}
	emitVal(res)
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: kg resolve \"<kind:name>\"")
	}
	e, err := open()
	if err != nil {
		return err
	}
	out, err := e.ResolveName(strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	emitVal(out)
	return nil
}

func cmdQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: kg query \"<cypher>\"")
	}
	e, err := open()
	if err != nil {
		return err
	}
	rows, err := e.Query(strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "rows": rows})
	return nil
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max hits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := open()
	if err != nil {
		return err
	}
	hits, err := e.Search(strings.Join(fs.Args(), " "), *limit)
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "hits": hits})
	return nil
}

func cmdContext(args []string) error {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	paths := fs.String("paths", "", "comma-separated code paths touched by current work")
	about := fs.String("about", "", "element name/kind of interest")
	max := fs.Int("max", 15, "max items")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := open()
	if err != nil {
		return err
	}
	res, err := e.Context(engine.ContextQuery{Paths: splitCSV(*paths), About: *about, Max: *max})
	if err != nil {
		return err
	}
	emitVal(res)
	return nil
}

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: kg history \"<element kind:name or id>\"")
	}
	e, err := open()
	if err != nil {
		return err
	}
	res, err := e.History(strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	emitVal(res)
	return nil
}

func cmdAsOf(args []string) error {
	fs := flag.NewFlagSet("as-of", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: kg as-of <timestamp>")
	}
	e, err := open()
	if err != nil {
		return err
	}
	res, err := e.AsOf(fs.Arg(0))
	if err != nil {
		return err
	}
	emitVal(res)
	return nil
}

func cmdConflicts(args []string) error {
	fs := flag.NewFlagSet("conflicts", flag.ContinueOnError)
	about := fs.String("about", "", "filter by element name substring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := open()
	if err != nil {
		return err
	}
	conf, err := e.Conflicts(*about)
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "conflicts": conf, "count": len(conf)})
	return nil
}

func cmdSync(args []string) error {
	e, err := open()
	if err != nil {
		return err
	}
	sr, applied, conf, err := e.Sync()
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "sync": sr, "applied": applied, "conflicts": conf, "conflict_count": len(conf)})
	return nil
}

func cmdRebuild(args []string) error {
	e, err := open()
	if err != nil {
		return err
	}
	n, err := e.Rebuild()
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "applied": n})
	return nil
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	canonical := fs.Bool("canonical", false, "deterministic canonical export for replay verification")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := open()
	if err != nil {
		return err
	}
	out, err := e.Export(*canonical)
	if err != nil {
		return err
	}
	emitVal(out)
	return nil
}

func cmdDoctor(args []string) error {
	e, err := open()
	if err != nil {
		return err
	}
	rep, err := e.Doctor()
	if err != nil {
		return err
	}
	emitVal(rep)
	return nil
}

// ---- output ----------------------------------------------------------------

func emit(v map[string]any) {
	if _, ok := v["ok"]; !ok {
		v["ok"] = true
	}
	emitVal(v)
}

func emitVal(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal error: %v\n", err)
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func usage() {
	fmt.Fprint(os.Stderr, `kg — kgai knowledge graph (domain elements shaped by an immutable decision log)

USAGE: kg <command> [flags]

WRITE
  init [--remote URL] [--actor NAME] [--root DIR]   initialize the store
  ingest [--file F] [--dry-run]                      record decision(s) + graph mutations from stdin JSON

READ
  context [--paths a,b] [--about X] [--max N]         relevant elements + the decisions that shaped them
  history "<element>"                                 full decision chain that shaped an element
  as-of <timestamp>                                   element-graph structure effective at a past time
  search "<text>" [--limit N]                         substring search over elements & decisions
  resolve "<kind:name>"                               resolve an element name to its deterministic id
  query "<cypher>"                                    raw Cypher (power users)
  conflicts [--about X]                               elements shaped by >1 head decision

ADMIN
  sync         commit + pull (ff/union, never rebase) + replay + push
  rebuild      discard graph cache and replay the whole log
  export --canonical   deterministic dump for replay verification
  doctor       verify hash chains and report store health
`)
}
