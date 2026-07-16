// Human-readable rendering for read commands when stdout is a terminal. Piped
// output (agents, scripts) keeps the stable JSON contract; `--json` forces JSON
// even on a TTY. Only presentation lives here — every value shown comes from the
// same structs the JSON path serializes.
package main

import (
	"fmt"
	"os"
	"strings"

	"kgai/internal/engine"
	"kgai/internal/remote"
)

var forceJSON bool

func pretty() bool {
	if forceJSON {
		return false
	}
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// ANSI helpers — colors only make sense on a TTY, which is the only path here.
const (
	cReset = "\x1b[0m"
	cDim   = "\x1b[2m"
	cBold  = "\x1b[1m"
	cGreen = "\x1b[32m"
	cRed   = "\x1b[31m"
	cCyan  = "\x1b[36m"
	cMag   = "\x1b[35m"
	cYel   = "\x1b[33m"
)

func day(rfc3339 string) string {
	if len(rfc3339) >= 10 {
		return rfc3339[:10]
	}
	return rfc3339
}

func shortID(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

func prettyIngest(res engine.IngestResult) {
	head := cGreen + "✓ recorded" + cReset
	if res.DryRun {
		head = cYel + "dry run — nothing written" + cReset
	}
	for _, d := range res.Decisions {
		fmt.Printf("%s %s“%s”%s  %s\n", head, cBold, d.Title, cReset, cDim+shortID(d.ID)+cReset)
		if len(d.Supersedes) > 0 {
			short := make([]string, len(d.Supersedes))
			for i, s := range d.Supersedes {
				short[i] = shortID(s)
			}
			fmt.Printf("    %ssupersedes %s — kept in history%s\n", cDim, strings.Join(short, ", "), cReset)
		}
	}
	if len(res.Elements) > 0 {
		names := make([]string, 0, len(res.Elements))
		for n := range res.Elements {
			names = append(names, n)
		}
		fmt.Printf("    %selements: %s%s\n", cDim, strings.Join(names, ", "), cReset)
	}
	for _, w := range res.Warnings {
		fmt.Printf("    %s⚠ %s%s\n", cYel, w, cReset)
	}
}

func prettySync(sr remote.SyncResult, applied, conflicts int) {
	switch {
	case sr.Detail != "":
		fmt.Printf("%s○ %s%s\n", cDim, sr.Detail, cReset)
	case !sr.Pushed && !sr.Pulled:
		fmt.Printf("%s✓ up to date%s %swith %s%s\n", cGreen, cReset, cDim, sr.Remote, cReset)
	default:
		fmt.Printf("%s✓ synced%s %swith %s%s\n", cGreen, cReset, cDim, sr.Remote, cReset)
		if sr.Pushed {
			fmt.Printf("  ↑ pushed local decisions\n")
		}
		if sr.Pulled {
			fmt.Printf("  ↓ pulled %d decision(s) from the team\n", applied)
		}
	}
	if conflicts > 0 {
		fmt.Printf("  %s⚠ %d semantic conflict(s) — see `kg conflicts`%s\n", cRed, conflicts, cReset)
	} else if sr.Pulled || sr.Pushed {
		fmt.Printf("  %s0 conflicts%s\n", cDim, cReset)
	}
}

func prettySearch(hits []engine.SearchHit) {
	if len(hits) == 0 {
		fmt.Println(cDim + "no matches" + cReset)
		return
	}
	for i, h := range hits {
		marker := cCyan + "●" + cReset
		if i > 0 {
			marker = cDim + "○" + cReset
		}
		kind := h.Kind
		if h.Kind == "element" && h.Extra != "" {
			kind = h.Extra + " " + kind
		}
		fmt.Printf("%s %s%s%s  %s%s%s\n", marker, cBold, h.Name, cReset, cDim, kind, cReset)
		if h.Kind == "decision" && h.Extra != "" {
			fmt.Printf("    %s\n", h.Extra)
		}
		if len(h.Elements) > 0 {
			fmt.Printf("    %s→ %s%s\n", cDim, strings.Join(h.Elements, ", "), cReset)
		}
	}
}

func prettyHistory(res engine.HistoryResult) {
	fmt.Printf("%s%s:%s%s — %d decision(s), oldest first\n\n", cBold, res.Kind, res.Name, cReset, len(res.Decisions))
	for _, d := range res.Decisions {
		status := cDim + "superseded" + cReset
		title := cDim + d.Title + cReset
		if d.IsHead {
			status = cGreen + "● current" + cReset
			title = cBold + d.Title + cReset
		}
		fmt.Printf("  %s%s%s  %s  %s\n", cDim, day(d.When), cReset, title, status)
		if d.Rationale != "" {
			fmt.Printf("      %swhy:%s %s\n", cCyan, cReset, d.Rationale)
		}
		fmt.Printf("      %sby %s · %s%s\n\n", cDim, d.Author, shortID(d.ID), cReset)
	}
}

func prettyContext(res engine.ContextResult) {
	for _, it := range res.Items {
		fmt.Printf("%s%s%s %s(%s)%s", cBold, it.Name, cReset, cDim, it.Kind, cReset)
		for k, v := range it.Props {
			fmt.Printf("  %s%s=%s%s", cMag, k, v, cReset)
		}
		fmt.Println()
		for _, l := range it.Links {
			arrow := "→"
			if l.Dir == "in" {
				arrow = "←"
			}
			fmt.Printf("    %s%s %s %s%s\n", cDim, arrow, l.Kind, l.Neighbor, cReset)
		}
		for _, w := range it.Why {
			if w.IsHead {
				fmt.Printf("    %s●%s %s%s%s — %s\n", cGreen, cReset, cBold, w.Title, cReset, w.Rationale)
			} else {
				fmt.Printf("    %s○ %s — %s%s\n", cDim, w.Title, w.Rationale, cReset)
			}
		}
	}
	if res.Omitted > 0 {
		fmt.Printf("%s(%d shown, %d more omitted — raise --max)%s\n", cDim, res.Shown, res.Omitted, cReset)
	}
	if res.Note != "" {
		fmt.Printf("%s%s%s\n", cDim, res.Note, cReset)
	}
}

func prettyConflicts(groups []engine.ConflictGroup) {
	if len(groups) == 0 {
		fmt.Println(cGreen + "✓ no conflicts" + cReset + cDim + " — every element has a single authoritative decision" + cReset)
		return
	}
	fmt.Printf("%s⚠ %d element(s) decided two ways%s\n\n", cRed, len(groups), cReset)
	for _, g := range groups {
		fmt.Printf("  %s%s:%s%s\n", cBold, g.Kind, g.Name, cReset)
		for i, t := range g.Titles {
			id := ""
			if i < len(g.Heads) {
				id = shortID(g.Heads[i])
			}
			fmt.Printf("    %s├─%s “%s”  %s%s%s\n", cDim, cReset, t, cDim, id, cReset)
		}
		fmt.Printf("    %s└─ resolve: record one decision superseding both%s\n\n", cDim, cReset)
	}
}
