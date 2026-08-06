// Command kg is the local CLI for the kgai knowledge graph: a small, stable graph of
// domain ELEMENTS (application & business things) shaped by an append-only, immutable
// log of DECISIONS. Decisions mutate the element graph and carry who/why/when, so the
// full evolution of every element is preserved and queryable.
//
// Output is JSON on stdout (the primary consumer is the AI via the plugin skill).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"kgai/internal/engine"
	"kgai/internal/store"
)

// version is the plugin release version, injected at build time via
// -ldflags "-X main.version=<v>" (from .claude-plugin/plugin.json). Defaults to
// "dev" for local/unstamped builds.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := make([]string, 0, len(os.Args)-2)
	for _, a := range os.Args[2:] {
		if a == "--json" {
			forceJSON = true
			continue
		}
		args = append(args, a)
	}
	if err := dispatch(os.Args[1], args); err != nil {
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
	case "remote":
		return cmdRemote(args)
	case "config":
		return cmdConfig(args)
	case "trust":
		return cmdTrust(args)
	case "prompt":
		// Shorthand for the one key hooks and skills read on every session.
		return cmdConfig(append([]string{"get"}, append(args, "prompt")...))
	case "rotate":
		return cmdRotate(args)
	case "rebuild":
		return cmdRebuild(args)
	case "export":
		return cmdExport(args)
	case "doctor":
		return cmdDoctor(args)
	case "status", "info":
		return cmdStatus(args)
	case "version", "-v", "--version":
		emit(map[string]any{"ok": true, "name": "kg", "version": version, "schema_version": store.SchemaVersion})
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// open opens the store for a WRITE-intent command (ingest, sync, setting a remote),
// lazily creating the per-project store on first use so recording a decision always
// works in a fresh project — no `kg init` required.
func open() (*engine.Engine, error) {
	// ResolveRoot, not the env var alone: a `store` setting that is broken, points
	// somewhere unusable, or has not been approved must stop the command. Falling back
	// to the per-project default would record the decision into a store nobody reads.
	root, err := store.ResolveRoot()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(root)
	if err != nil {
		s, err = store.Init(root, "", "")
		if err != nil {
			return nil, err
		}
	}
	return engine.New(s), nil
}

// openRead opens the store for a READ command WITHOUT creating it. Running a read in
// a directory with no store (e.g. outside any project) must not silently mint a new
// empty graph there — that forks the project's memory. A missing store returns
// (nil, nil); callers emit their empty result shape plus noStoreNote() so an agent
// reads a clean "nothing recorded here yet" instead of an error.
func openRead() (*engine.Engine, error) {
	root, err := store.ResolveRoot()
	if err != nil {
		return nil, err // a broken/unapproved `store` setting is loud on reads too
	}
	s, err := store.Open(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no store yet — NOT an error, and nothing is created
		}
		return nil, err // real problem (e.g. corrupt config) must stay loud
	}
	return engine.New(s), nil
}

func noStoreNote() string {
	root, source, err := store.ResolveRootWithSource()
	if err != nil {
		return err.Error()
	}
	if source != "" {
		// A store someone deliberately pointed at is missing. Saying "nothing recorded
		// yet" here reads as "the team has no history", which is how an agent ends up
		// telling the user their graph is empty when it is merely unreachable.
		where := "the " + source + " layer"
		if source == "KGAI_STORE" {
			where = "KGAI_STORE"
		}
		return fmt.Sprintf("no knowledge graph store at %s, which is where %s says this project's store lives. Either nothing has been recorded there yet, or that path is not reachable from this machine (a shared store that is not mounted, a path that does not exist, no permission) — check before concluding the project has no history.", root, where)
	}
	return fmt.Sprintf("no knowledge graph store at %s — nothing recorded for this project yet. The store is created automatically by the first recorded decision (kg ingest) or by kg init.", root)
}

// noStore emits the empty result for a read command against a missing store: the
// command's natural empty shape merged with an explanatory note.
func noStore(extra map[string]any) error {
	if pretty() {
		fmt.Println(cDim + noStoreNote() + cReset)
		return nil
	}
	m := map[string]any{"ok": true, "note": noStoreNote()}
	for k, v := range extra {
		m[k] = v
	}
	emit(m)
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	root := fs.String("root", "", "store root (default: $KGAI_STORE or <project>/.kgai/store)")
	actor := fs.String("actor", "", "actor/author name for this install")
	remote := fs.String("remote", "", "sync remote: s3://bucket/prefix[?profile=NAME&region=REGION] (supported; profile pins an AWS/SSO profile), git URL (experimental), kgai://org/project (beta)")
	token := fs.String("token", "", "kgai cloud token (stored install-locally, 0600)")
	cloudURL := fs.String("cloud-url", "", "kgai cloud broker base URL (overridable by KGAI_CLOUD_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := store.Init(*root, *actor, *remote)
	if err != nil {
		return err
	}
	if *token != "" || *cloudURL != "" {
		if *token != "" {
			s.Config.CloudToken = *token
		}
		if *cloudURL != "" {
			s.Config.CloudURL = *cloudURL
		}
		if err := s.SaveConfig(); err != nil {
			return err
		}
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
	// Unknown fields are rejected, not silently dropped: a model that invents a field
	// (typically mirroring the OUTPUT shape, e.g. "elements") would otherwise record a
	// decision with no mutations and never learn why it is unfindable.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return fmt.Errorf("invalid ingest JSON: %w — valid decision fields are title, rationale, author, date, refs, supersedes_on, mutations; elements are attached via mutations, e.g. {\"op\":\"upsert_element\",\"kind\":\"service\",\"name\":\"X\"}", err)
		}
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
	if pretty() {
		prettyIngest(res)
		return nil
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"existed": false})
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"rows": []any{}})
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"hits": []any{}})
	}
	hits, err := e.Search(strings.Join(fs.Args(), " "), *limit)
	if err != nil {
		return err
	}
	if pretty() {
		prettySearch(hits)
		return nil
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"items": []any{}, "shown": 0, "omitted": 0, "total": 0})
	}
	res, err := e.Context(engine.ContextQuery{Paths: splitCSV(*paths), About: *about, Max: *max})
	if err != nil {
		return err
	}
	if pretty() {
		prettyContext(res)
		return nil
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"decisions": []any{}})
	}
	res, err := e.History(strings.Join(fs.Args(), " "))
	if err != nil {
		return err
	}
	if pretty() {
		prettyHistory(res)
		return nil
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"elements": []any{}, "links": []any{}})
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"conflicts": []any{}, "count": 0})
	}
	conf, err := e.Conflicts(*about)
	if err != nil {
		return err
	}
	if pretty() {
		prettyConflicts(conf)
		return nil
	}
	emit(map[string]any{"ok": true, "conflicts": conf, "count": len(conf)})
	return nil
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	auto := fs.Bool("auto", false, "background mode: exit silently when there is no store, no remote, a fresh cooldown stamp, or a held lock — never create anything, never block")
	cooldown := fs.Int("cooldown", 60, "with --auto: skip if a sync was attempted fewer than this many seconds ago")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *auto {
		// Fired blindly by hooks on every session start and turn end. Everything
		// that makes it a no-op must be silent and fast.
		e, err := openRead()
		if err != nil || e == nil {
			return nil // no store here (or unreadable) — a background job stays quiet
		}
		if url, _ := e.S.EffectiveRemote(); url == "" {
			return nil
		}
		ran, sr, applied, conf, err := e.SyncAuto(time.Duration(*cooldown) * time.Second)
		if err != nil {
			return err // JSON error → the hook's log redirect; last-autosync.json has it too
		}
		if ran {
			emit(map[string]any{"ok": true, "sync": sr, "applied": applied, "conflict_count": len(conf)})
		}
		return nil
	}

	e, err := open()
	if err != nil {
		return err
	}
	sr, applied, conf, err := e.Sync()
	if err != nil {
		return err
	}
	if pretty() {
		prettySync(sr, applied, len(conf))
		return nil
	}
	emit(map[string]any{"ok": true, "sync": sr, "applied": applied, "conflicts": conf, "conflict_count": len(conf)})
	return nil
}

// cmdRemote shows or sets the sync remote — a thin view onto the layered config
// (`kg config … remote`), kept because setting a remote is the one configuration step
// almost every user performs. With no arguments it reports every layer plus the
// effective value; a URL sets it in the session layer, --project in the repo's
// committed .kgairc, --global machine-wide. The sentinel "none" opts a project out
// of a remote inherited from a broader layer.
func cmdRemote(args []string) error {
	fs := flag.NewFlagSet("remote", flag.ContinueOnError)
	global := fs.Bool("global", false, "operate on the machine-wide layer (<KGAI_HOME>/config.json)")
	project := fs.Bool("project", false, "operate on the repo's committed layer (<repo>/.kgairc)")
	session := fs.Bool("session", false, "operate on this install's layer (<store>/kg.config.json) — the default")
	unset := fs.Bool("unset", false, "clear the remote instead of setting it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectTrailingFlags("remote", fs.Args()); err != nil {
		return err
	}
	url := fs.Arg(0)
	if *unset && url != "" {
		return fmt.Errorf("--unset takes no URL argument")
	}
	scope, err := configScope(*session, *project, *global)
	if err != nil {
		return err
	}

	if *unset || url != "" {
		if err := writeConfig(scope, "remote", url); err != nil {
			return err
		}
	}
	return showRemote()
}

// showRemote reports the remote per layer and the effective result. Showing state is a
// read — it must not create a store, so a missing one reports the broader layers alone.
func showRemote() error {
	layers, storeErr := loadLayers()
	per := map[string]any{}
	for _, l := range layers {
		per[l.Name] = l.Settings.Remote
	}
	effective, source := store.Effective(layers, "remote")
	if effective == store.RemoteNone {
		effective, source = "", "disabled"
	} else if effective != "" {
		effective = store.ExpandRemote(effective)
	}
	out := map[string]any{
		"session":   per[store.LayerSession],
		"project":   per[store.LayerProject],
		"global":    per[store.LayerGlobal],
		"effective": effective,
		"source":    source,
	}
	if storeErr != "" {
		out["store_error"] = storeErr
	}
	emit(out)
	return nil
}

// cmdConfig reads and writes the layered configuration (session > project > global,
// see store/settings.go). Flags precede the subcommand's arguments, as everywhere else
// in this CLI:
//
//	kg config                                  every layer + each effective value and its source
//	kg config get [--raw] <key>                one key, with the layer it came from
//	kg config set [--scope] <key> <value|->     - reads the value from stdin
//	kg config unset [--scope] <key>
//
// Scope defaults to the session layer. --project writes the repo's committed .kgairc;
// --global writes this machine's default, which is otherwise never touched.
func cmdConfig(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	global := fs.Bool("global", false, "operate on the machine-wide layer (<KGAI_HOME>/config.json)")
	project := fs.Bool("project", false, "operate on the repo's committed layer (<repo>/.kgairc)")
	session := fs.Bool("session", false, "operate on this install's layer (<store>/kg.config.json) — the default")
	raw := fs.Bool("raw", false, "print the value alone, unquoted (get)")
	fromFile := fs.String("from-file", "", "read the value from a file (set)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	scope, err := configScope(*session, *project, *global)
	if err != nil {
		return err
	}

	switch sub {
	case "", "show", "list":
		return showConfig()
	case "get":
		if err := rejectTrailingFlags("config get", fs.Args()); err != nil {
			return err
		}
		// A scope on `get` asks a different question: not "what is in effect" but
		// "what does THIS layer hold" — which is how you check what a clone inherits.
		scoped := ""
		if *session || *project || *global {
			scoped = scope
		}
		return getConfig(fs.Arg(0), *raw, scoped)
	case "set", "unset":
		// Only the key position and anything beyond the value: a VALUE may legitimately
		// start with "-" (a capture rule written as a bullet list is the normal case).
		check := fs.Args()
		if sub == "set" && len(check) > 1 {
			check = append([]string{check[0]}, check[2:]...)
		}
		if err := rejectTrailingFlags("config "+sub, check); err != nil {
			return err
		}
		key := fs.Arg(0)
		if key == "" {
			return fmt.Errorf("%s needs a key — one of: %s", sub, strings.Join(store.SettingKeys, ", "))
		}
		val := ""
		if sub == "set" {
			if err := rejectFlagAsValue("config set", fs, fs.Arg(1)); err != nil {
				return err
			}
			if val, err = configValue(fs.Arg(1), *fromFile); err != nil {
				return err
			}
			if val == "" {
				return fmt.Errorf("set needs a value: `kg config set %s <value>`, `--from-file F`, or `-` to read stdin (use `kg config unset %s` to clear it)", key, key)
			}
		}
		// Changing where the store lives can strand an existing log: the decisions are
		// still on disk, just not where anything looks any more. Say so once, here.
		before, _ := store.ResolveRoot()
		if err := writeConfig(scope, key, val); err != nil {
			return err
		}
		note := ""
		if key == "store" {
			if after, err := store.ResolveRoot(); err == nil && after != before && store.HasEvents(before) {
				note = fmt.Sprintf("the previous store at %s still holds recorded decisions — they are not visible from this repo any more. To carry them over, copy its log/*.ndjson into %s/log/ and run `kg rebuild` (docs/SHARED-STORE.md).", before, after)
			}
		}
		return showConfigWithNote(note)
	}
	return fmt.Errorf("unknown config subcommand %q — use show, get, set or unset", sub)
}

// cmdTrust approves this repository's committed .kgairc — the one config layer that
// arrives with a clone rather than being written by the person running kgai. Until it
// is approved it decides nothing; approval is bound to the file's content, so any later
// edit or pulled commit asks again.
//
//	kg trust            show what the file asks for, and approve it
//	kg trust --show     show it without approving (what a hook tells the user to read)
//	kg trust --revoke   withdraw approval
//	kg trust --list     every file approved on this machine
func cmdTrust(args []string) error {
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	show := fs.Bool("show", false, "print what the file asks for without approving it")
	revoke := fs.Bool("revoke", false, "withdraw approval for the configuration this repo asks for")
	list := fs.Bool("list", false, "list every configuration approved on this machine")
	ack := fs.Bool("ack", false, "record that this repo uses an already-approved configuration (announced once)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := store.ProjectConfigPath()
	if *list {
		recs, err := store.TrustedConfigs()
		if err != nil {
			return err
		}
		emit(map[string]any{"trusted": recs, "count": len(recs),
			"note": "approval is per CONFIGURATION, not per repo: `paths` says where each was accepted from, and every repo asking for the same thing is covered"})
		return nil
	}
	if *ack {
		if err := store.Ack(path); err != nil {
			return err
		}
		emit(map[string]any{"path": path, "acknowledged": true})
		return nil
	}

	if *revoke {
		had, err := store.Untrust(path)
		if err != nil {
			return err
		}
		emit(map[string]any{"path": path, "revoked": had,
			"note": "that configuration no longer decides anything on this machine — including in other repos that asked for the same thing; `kg trust` re-approves it"})
		return nil
	}

	// What is being approved, in full — approving a hash nobody read is not consent.
	var st store.Settings
	var exists bool
	if err := store.ReadConfigFile(path, &st, &exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("no %s in this repository — there is nothing to approve", path)
	}
	asks := map[string]any{}
	if st.StoreRoot != "" {
		resolved, err := store.ExpandStorePath(st.StoreRoot)
		if err != nil {
			asks["store"] = map[string]any{"value": st.StoreRoot, "error": err.Error()}
		} else {
			asks["store"] = map[string]any{"value": st.StoreRoot, "resolves_to": resolved}
		}
	}
	if st.Prompt != "" {
		asks["prompt"] = st.Prompt
	}
	ignored := []string{}
	for _, key := range store.SettingKeys {
		if v, _ := st.Get(key); v != "" && !store.KeyAllowedIn(key, store.LayerProject) {
			ignored = append(ignored, key)
		}
	}
	ts, err := store.TrustStateOf(path)
	if err != nil {
		return err
	}
	out := map[string]any{"path": path, "asks_for": asks, "already_approved": ts.Trusted}
	if ts.InheritedFrom != "" {
		out["approval_inherited_from"] = ts.InheritedFrom
		out["inherited_note"] = "this is the same configuration you already approved there, so it is already in effect here"
	}
	if len(ignored) > 0 {
		out["ignored_keys"] = ignored
		out["ignored_note"] = "these keys are never taken from a committed file and are ignored whether or not you approve it"
	}
	if *show {
		out["note"] = "run `kg trust` to approve; until then this file decides nothing"
		emit(out)
		return nil
	}
	fp, err := store.Trust(path)
	if err != nil {
		return err
	}
	out["approved"] = true
	out["fingerprint"] = fp
	out["note"] = "approved for this machine; any later change to the file asks again"
	emit(out)
	return nil
}

// rejectTrailingFlags catches a flag written AFTER a positional argument. Go's flag
// package stops parsing at the first non-flag word, so `kg config set remote --project
// s3://x/y` silently stored the literal "--project" as the remote and dropped the URL,
// and `kg remote s3://team/kg --global` wrote the session layer while reporting success.
// Both looked like they had worked. A misplaced flag is a mistake, not a value.
func rejectTrailingFlags(cmd string, args []string) error {
	for _, a := range args {
		if len(a) > 1 && strings.HasPrefix(a, "-") {
			return fmt.Errorf("%q looks like a flag but came after an argument, where it would be stored as a value instead of taking effect — put it before the arguments (`kg %s %s …`)", a, cmd, a)
		}
	}
	return nil
}

// rejectFlagAsValue guards the one position that may legitimately start with "-": a
// capture rule written as a bullet list is an ordinary value, but `--from-file` in that
// slot is somebody's misplaced flag, and storing it silently writes nonsense into a
// COMMITTED file the whole team then clones and approves. Enumerated from the FlagSet,
// so a flag added later is covered without anyone remembering this function.
func rejectFlagAsValue(cmd string, fs *flag.FlagSet, val string) error {
	if !strings.HasPrefix(val, "-") {
		return nil
	}
	known := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { known["-"+f.Name], known["--"+f.Name] = true, true })
	name := val
	if i := strings.IndexAny(name, "= "); i > 0 {
		name = name[:i]
	}
	if known[name] {
		return fmt.Errorf("%q is a flag of this command but was written where the value goes, so it would be stored as the value — put it before the arguments (`kg %s %s … <key>`)", val, cmd, val)
	}
	return nil
}

func configScope(session, project, global bool) (string, error) {
	n := 0
	for _, b := range []bool{session, project, global} {
		if b {
			n++
		}
	}
	if n > 1 {
		return "", fmt.Errorf("--session, --project and --global are mutually exclusive — a value is written to exactly one layer")
	}
	switch {
	case project:
		return store.LayerProject, nil
	case global:
		return store.LayerGlobal, nil
	}
	return store.LayerSession, nil
}

// configValue takes the value from --from-file, from stdin ("-"), or verbatim. Files
// and stdin keep multi-line text (a prompt) readable at the shell.
func configValue(arg, fromFile string) (string, error) {
	var b []byte
	var err error
	switch {
	case fromFile != "":
		b, err = os.ReadFile(fromFile)
	case arg == "-":
		b, err = io.ReadAll(os.Stdin)
	default:
		return arg, nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// loadLayers resolves the layers for a read. Reading configuration must never mint a
// store: `kg config` answers the same before and after `kg init`.
//
// storeErr carries a store that could not be resolved (a `store` setting pointing
// somewhere unusable, a corrupt .kgairc) WITHOUT failing: `kg config` is the command
// people run to find out what is wrong, so it has to keep working when something is.
func loadLayers() ([]store.Layer, string) {
	e, err := openRead()
	if err != nil {
		layers, lerr := store.LoadLayers(nil)
		if lerr != nil {
			// Even the files themselves don't parse — report that instead.
			return []store.Layer{{Name: store.LayerSession}, {Name: store.LayerProject}, {Name: store.LayerGlobal}}, lerr.Error()
		}
		return layers, err.Error()
	}
	var s *store.Store
	if e != nil {
		s = e.S
	}
	layers, lerr := store.LoadLayers(s)
	if lerr != nil {
		return []store.Layer{{Name: store.LayerSession}, {Name: store.LayerProject}, {Name: store.LayerGlobal}}, lerr.Error()
	}
	return layers, ""
}

func showConfig() error { return showConfigWithNote("") }

func showConfigWithNote(extra string) error {
	layers, storeErr := loadLayers()
	effective := map[string]any{}
	sources := map[string]any{}
	for _, k := range store.SettingKeys {
		v, src := store.Effective(layers, k)
		effective[k], sources[k] = v, src
	}
	// store_root is the RESOLVED path (placeholders expanded, KGAI_STORE honored) —
	// the answer to "is this repo really using the shared graph?", which the raw
	// `store` value alone does not give.
	out := map[string]any{
		"effective": effective,
		"sources":   sources,
		"layers":    layers,
	}
	if root, err := store.ResolveRoot(); err == nil {
		out["store_root"] = root
	} else {
		// The store cannot be resolved — say so here rather than failing, because this
		// is the command someone runs to find out why.
		out["store_error"] = err.Error()
	}
	if storeErr != "" && out["store_error"] == nil {
		out["store_error"] = storeErr
	}
	if extra != "" {
		out["previous_store"] = extra
	}
	// A repo config waiting for approval is reported, never silently ignored: an
	// unexplained fallback to the per-project store is how decisions end up in a graph
	// nobody looks at.
	for _, l := range layers {
		if l.Pending {
			out["pending_approval"] = l.Path
			out["note"] = "this repo's .kgairc has not been approved on this machine, so it decides nothing yet — `kg trust --show` prints what it asks for, `kg trust` approves it"
		}
		if l.InheritedFrom != "" {
			out["approval_inherited_from"] = l.InheritedFrom
		}
	}
	emit(out)
	return nil
}

func getConfig(key string, raw bool, layer string) error {
	if key == "" {
		return fmt.Errorf("get needs a key — one of: %s", strings.Join(store.SettingKeys, ", "))
	}
	layers, _ := loadLayers()
	// Validate the key against a layer so a typo fails loudly instead of reporting
	// "unset" — the difference matters when a hook acts on the answer.
	if _, err := layers[0].Settings.Get(key); err != nil {
		return err
	}
	val, source := store.Effective(layers, key)
	if layer != "" {
		// Report that layer alone, empty included — "the project layer sets nothing" is
		// exactly the answer someone is looking for when they ask this way.
		val, source = "", layer
		for _, l := range layers {
			if l.Name == layer {
				v, err := l.Settings.Get(key)
				if err != nil {
					return err
				}
				val = v
			}
		}
	}
	pending := ""
	for _, l := range layers {
		if l.Pending {
			pending = l.Path
		}
	}
	if raw {
		// Plain text for hooks and scripts: the value alone, nothing to parse. An
		// unset key prints nothing and still exits 0 — "nothing configured" is a
		// normal state, not a failure.
		if val != "" {
			fmt.Println(val)
		}
		return nil
	}
	out := map[string]any{"key": key, "value": val, "source": source}
	for _, l := range layers {
		if l.InheritedFrom != "" {
			out["approval_inherited_from"] = l.InheritedFrom
		}
	}
	if pending != "" {
		// Same reason as in showConfig: a config that is being ignored has to say so,
		// on every surface that reports its value.
		out["pending_approval"] = pending
	}
	emit(out)
	return nil
}

// writeConfig persists one key into one layer. Callers report the resulting state
// themselves, so `kg remote` can keep its remote-shaped output.
func writeConfig(scope, key, val string) error {
	if err := store.ValidateLayerKey(scope, key); err != nil {
		return err
	}
	switch scope {
	case store.LayerSession:
		// The session layer lives inside the store, which owns its own locking and
		// identity fields — go through the store, not the file.
		var e *engine.Engine
		var err error
		if val == "" {
			// Clearing: nothing to clear if no store exists — don't create one for it.
			if e, err = openRead(); err != nil {
				return err
			}
			if e == nil {
				return nil
			}
		} else if e, err = open(); err != nil {
			// Writing configuration is deliberate — creating the store is intended.
			return err
		}
		return e.S.UpdateConfig(func(c *store.Config) error { return c.Settings.Set(key, val) })
	case store.LayerProject:
		return store.WriteLayer(scope, store.ProjectConfigPath(), key, val)
	case store.LayerGlobal:
		if key == "remote" && val == store.RemoteNone {
			return fmt.Errorf("%q opts a project out of a remote inherited from a broader layer; the global layer has nothing above it — use `kg config unset --global remote`", store.RemoteNone)
		}
		// Through WriteLayer, so the machine-wide file keeps any key this version does
		// not know — the same reason the project layer goes through it.
		return store.WriteLayer(scope, store.GlobalConfigPath(), key, val)
	}
	return fmt.Errorf("unknown layer %q", scope)
}

func cmdRotate(args []string) error {
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"rotated": false})
	}
	if err := e.S.Lock(); err != nil {
		return err
	}
	defer e.S.Unlock()
	old, cur, err := e.S.RotateInstall()
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "rotated": true, "old_install": old, "install": cur,
		"note": "run `kg sync` — local-only decisions from the old identity are re-recorded automatically"})
	return nil
}

func cmdRebuild(args []string) error {
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"applied": 0})
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
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(nil)
	}
	out, err := e.Export(*canonical)
	if err != nil {
		return err
	}
	emitVal(out)
	return nil
}

func cmdDoctor(args []string) error {
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"initialized": false})
	}
	rep, err := e.Doctor()
	if err != nil {
		return err
	}
	emitVal(rep)
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openRead()
	if err != nil {
		return err
	}
	if e == nil {
		return noStore(map[string]any{"initialized": false, "version": version})
	}
	rep, err := e.Status()
	if err != nil {
		return err
	}
	rep.Version = version
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

WHAT THIS IS
  The live graph is a small set of ELEMENTS — domain things (feature:Invoice,
  service:Billing), not files — joined by LINKS (PART_OF, DEPENDS_ON, RENDERS…).
  A DECISION is an immutable event that MUTATES that graph (adds an element, adds
  or retires a link, sets a property) and carries who decided, why, and when.
  Nothing is ever edited or deleted: recording a new decision SUPERSEDES the
  previous one on the elements it governs, so the graph shows the current shape
  and the log keeps the whole story ("kg history", "kg as-of <date>").
  Two decisions taking authority over one element concurrently = a conflict
  ("kg conflicts"), resolved by one decision that takes authority again.

TYPICAL FLOW
  Before changing code   kg context --paths "src/billing/*"   what governs this area
                         kg search "how is invoicing structured"
  After a real decision  kg ingest <<'JSON' … JSON            one call, all mutations
  Answering "why is X"   kg history "feature:Invoice"

  What belongs in the log: structural choices about the domain — split/merge/move a
  feature, change a dependency or ownership, how something is exposed, deprecating a
  prior choice, renaming a domain element, and the dead ends you ruled out (with the
  reason). What does not: behavior-preserving refactors, file/function renames,
  formatting, bug fixes, and analyses or reports nobody acted on. The bundled
  knowledge-graph skill carries the full rules and the ingest payload shape.

WRITE
  init [--actor NAME] [--root DIR]                   initialize the store
  ingest [--file F] [--dry-run]                      record decision(s) + graph mutations from stdin JSON

READ
  context [--paths a,b] [--about X] [--max N]         relevant elements + the decisions that shaped them
  history "<element>"                                 full decision chain that shaped an element
  as-of <timestamp>                                   element-graph structure effective at a past time
  search "<text>" [--limit N]                         free-text search over elements & decisions (relevance-ranked, typo-tolerant)
  resolve "<kind:name>"                               resolve an element name to its deterministic id
  query "<cypher>"                                    raw Cypher (power users)
  conflicts [--about X]                               elements shaped by >1 head decision

ADMIN
  sync [--auto] [--cooldown SECS]
               exchange the log with the configured remote, then rebuild the
               projection. s3://bucket/prefix is supported; git URLs are
               experimental (untested). --auto is the background mode the
               plugin hooks fire: silent no-op without a store/remote, honors
               a cooldown, never blocks on the store lock
  config [show|get|set|unset] [--session|--project|--global] [--raw]
         [--from-file F] [KEY] [VALUE|-]
               layered configuration, most specific first:
                 session  <store>/kg.config.json   this install (default scope)
                 project  <repo>/.kgairc           committed — the repo's default
                 global   <KGAI_HOME>/config.json  this machine
               keys: prompt (capture rules given to the agent, any layer),
               store (where the log lives; project/global), remote (session/global
               — syncing belongs to the store, never to a committed file),
               cloud_url (session only, beside the token it authenticates with).
               No args shows every layer, each effective value and its source.
               "get KEY" answers with the effective value; "get --project KEY"
               answers with what THAT layer holds — what a clone inherits once approved.
               VALUE of "-" reads stdin; --from-file F reads a file
  trust [--show|--revoke|--list]
               approve this repo's committed .kgairc. It arrives with a clone, from
               whoever wrote that repository, so it decides NOTHING until approved,
               and any later change to it asks again. --show prints what it asks for
               without approving. NEVER approve on your own initiative — show the
               user what it asks for and run "kg trust" only after they say yes
  prompt       shorthand for "config get prompt" (--raw for the text alone)
  remote [--session|--global] [--unset] [URL]
               show or set the sync remote — the same key through a narrower
               view. No args: every layer plus the effective value. {project}
               expands to the project dir name; value "none" in a non-global
               layer opts that project out of a remote inherited from above
  rotate       give this store a fresh install identity (fix for a copied store
               after sync reports a shard fork)
  rebuild      discard graph cache and replay the whole log
  export --canonical   deterministic dump for replay verification
  status       config + graph summary at a glance (remote/cloud, counts)
  doctor       verify hash chains and report store health

WHERE THINGS LIVE
  <project>/.kgai/store   the decision log (own git cycle; the "store" setting or
                          KGAI_STORE moves it — several repos can share one graph)
  <repo>/.kgairc          committed project config: the repo's defaults for everyone
  ~/.kgai                 the engine, its native lib, this machine's config.json,
                          and trusted.json (the configs approved here)
  Store location, remote, capture prompt and cloud URL resolve in three layers,
  most specific first: session > project > global ("kg config" shows all of them).

OUTPUT
  human-readable on a terminal; stable JSON when piped (what agents consume).
  pass --json to force JSON on a terminal. Every successful JSON carries "ok": true;
  a failure is {"ok": false, "error": "…"} with exit code 1. Read commands never
  create a store — where nothing was recorded they answer empty and say so, so an
  empty result means "nothing recorded yet", never "something went wrong".
`)
}
