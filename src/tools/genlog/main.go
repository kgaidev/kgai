// genlog fabricates a realistic synced-team log at benchmark scale, emitting it as
// ready-to-upload segment objects (segments/<install>/000001-<count>.ndjson). Events
// carry valid content hashes, per-shard parent chains, and per-element supersede
// chains — exactly what a team of N users would have pushed — so cold-clone, rebuild,
// query and sync benchmarks run against honest data without paying the interactive
// ingest path for a million decisions.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kgai/internal/event"
)

func main() {
	total := flag.Int("total", 100000, "number of decisions")
	installs := flag.Int("installs", 30, "number of writers (installs/users)")
	elems := flag.Int("elems", 10000, "number of distinct elements")
	out := flag.String("out", "", "output dir (segments/<install>/... is created inside)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "need -out")
		os.Exit(1)
	}

	type shard struct {
		w        *bufio.Writer
		f        *os.File
		lastHash string
		count    int
		tmp      string
		install  string
	}
	shards := make([]*shard, *installs)
	for i := range shards {
		install := fmt.Sprintf("igen%012d", i)
		tmp := filepath.Join(*out, install+".part")
		f, err := os.Create(tmp)
		if err != nil {
			panic(err)
		}
		shards[i] = &shard{w: bufio.NewWriterSize(f, 1<<20), f: f, tmp: tmp, install: install}
	}

	heads := make(map[string]string, *elems) // element id → current head decision id
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for n := 0; n < *total; n++ {
		sh := shards[n%len(shards)]
		name := fmt.Sprintf("E%06d", n%*elems)
		elID := event.ElementID("feature", name)

		muts := []event.Mutation{{Op: event.MutUpsertElement, ElementID: elID, Kind: "feature", Name: name,
			Props: map[string]string{"batch": fmt.Sprintf("%d", n/1000)}}}
		shapes := []string{elID}
		if n%3 == 0 {
			tn := fmt.Sprintf("E%06d", (n*7)%*elems)
			tID := event.ElementID("feature", tn)
			muts = append(muts,
				event.Mutation{Op: event.MutUpsertElement, ElementID: tID, Kind: "feature", Name: tn},
				event.Mutation{Op: event.MutAddLink, ElementID: elID, FromID: elID, ToID: tID, LinkKind: "DEPENDS_ON"})
			shapes = append(shapes, tID)
		}
		if n%5 == 0 {
			muts = append(muts, event.Mutation{Op: event.MutSetProp, ElementID: elID, Key: "rev", Value: fmt.Sprintf("%d", n)})
		}

		d := event.Decision{
			Title:     fmt.Sprintf("decision %d: reshape %s", n, name),
			Rationale: fmt.Sprintf("bench decision by %s", sh.install),
			Author:    sh.install,
			Shapes:    shapes,
			Targets:   []string{elID},
			Mutations: muts,
		}
		if prev, ok := heads[elID]; ok {
			d.Supersedes = []string{prev}
		}
		d.ID = event.DecisionID(d)
		heads[elID] = d.ID

		ev := event.Event{
			Op:         event.OpAssert,
			Actor:      sh.install,
			InstallID:  sh.install,
			Lamport:    int64(n + 1),
			RecordedAt: base.Add(time.Duration(n) * time.Second).Format(time.RFC3339),
			Decision:   &d,
		}
		if sh.lastHash != "" {
			ev.Parents = []string{sh.lastHash}
		}
		ev.ComputeHash()
		sh.lastHash = ev.Hash
		sh.count++

		line, err := json.Marshal(ev)
		if err != nil {
			panic(err)
		}
		sh.w.Write(line)
		sh.w.WriteByte('\n')
	}

	for _, sh := range shards {
		sh.w.Flush()
		sh.f.Close()
		dir := filepath.Join(*out, "segments", sh.install)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			panic(err)
		}
		final := filepath.Join(dir, fmt.Sprintf("%06d-%d.ndjson", 1, sh.count))
		if err := os.Rename(sh.tmp, final); err != nil {
			panic(err)
		}
	}
	fmt.Printf("generated %d decisions across %d installs into %s\n", *total, *installs, *out)
}
