// Package turn records what a single agent turn did — did it edit code, did it already
// record a decision — so the end-of-turn hook can decide whether to demand a capture
// decision before the turn is allowed to end.
//
// Why the turn is observed as it happens rather than reconstructed afterwards: every host
// keeps a different transcript. Claude Code writes one JSONL shape, Codex writes a rollout
// log in an unrelated one, and Gemini's end-of-turn event carries no transcript at all.
// Reading them would be three parsers against three undocumented, moving formats. The
// hook payloads, by contrast, agree on the three fields this needs (session_id, tool_name,
// tool_input) and are a documented contract on all three.
//
// Nothing here touches the store or the graph: a mark is appended to a small per-session
// file, and the end-of-turn read consumes it. That is what keeps this callable after every
// single tool call without anyone noticing the cost.
package turn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Marks is what one turn did, as the end-of-turn hook needs to see it.
type Marks struct {
	Edited   int  `json:"edited"`
	Recorded bool `json:"recorded"`
	Found    bool `json:"found"` // false = nothing was ever marked for this session
}

// Event is the part of a hook payload this package reads. Every host sends these under
// the same names; everything else in the payload is ignored.
type Event struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// The union of the tools that write files, across hosts: Claude Code, then Codex, then
// Gemini CLI. A name only reaches this classifier when the host's matcher let it through,
// so the union is the safety net rather than the filter.
var editTools = regexp.MustCompile(`^(Edit|Write|MultiEdit|NotebookEdit` +
	`|apply_patch|edit_file|create_file|str_replace|update_file` +
	`|write_file|replace)$`)

// `exec` is Codex 0.154's shell tool (seen in a real session's rollout); the rest cover
// older Codex builds and the other hosts.
var shellTools = regexp.MustCompile(`^(Bash|shell|local_shell|unified_exec|exec|exec_command|run_shell_command)$`)

// Codex edits through apply_patch, but it also edits through the shell — a heredoc, a
// `sed -i`, a `git apply` — and so does Gemini. Those turns edit code just as much, and
// without this they would never be offered the capture nudge. Deliberately narrow: a false
// positive costs one extra end-of-turn nudge, a false negative costs a lost decision, so
// it errs toward firing.
var shellEdit = regexp.MustCompile(`(^|[;&|(]\s*)(sed\s+-i|perl\s+-i|patch\b|git\s+apply\b|tee\b|install\b|cp\b|mv\b|touch\b)` +
	`|>\s*[^|>\s&]+` + // a redirect that writes a file, heredocs included
	`|\bapply_patch\b`)

// `some-command >/dev/null 2>&1` is how half of all shell calls silence themselves.
// Counting it as an edit would demand a capture decision at the end of turns that only
// ever read — and a nudge that fires when there is nothing to record is a nudge the model
// learns to ignore. Go's regexp has no lookahead, so these are removed before matching
// rather than excluded inside the pattern.
var devNull = regexp.MustCompile(`>>?\s*/dev/null`)

// Anything outside this set would let a session id chosen by the host escape into a path.
var unsafeInSessionID = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Classify reads one PostToolUse/AfterTool payload and returns what it proves about the
// turn: "edit", "ingest", both, or nothing.
func Classify(payload []byte) []string {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil
	}
	var marks []string
	switch {
	case editTools.MatchString(ev.ToolName):
		marks = append(marks, "edit")
	case shellTools.MatchString(ev.ToolName):
		// Only the command the model ASKED to run counts. Scanning the whole payload would
		// also see tool OUTPUT, and `kg ingest` appears verbatim in this plugin's own skill
		// file — one `cat SKILL.md` would then look like a recorded decision and silence the
		// capture nudge for the rest of the turn.
		for _, cmd := range commandStrings(ev.ToolInput) {
			if strings.Contains(cmd, "kg ingest") || strings.Contains(cmd, "kg-decision") {
				marks = appendOnce(marks, "ingest")
			}
			if shellEdit.MatchString(devNull.ReplaceAllString(cmd, "")) {
				marks = appendOnce(marks, "edit")
			}
		}
	}
	return marks
}

func appendOnce(marks []string, m string) []string {
	for _, have := range marks {
		if have == m {
			return marks
		}
	}
	return append(marks, m)
}

// commandStrings pulls the strings the model asked the shell to run out of a tool_input of
// any shape. Anchors like ^ and ; only mean what they mean against the command itself —
// matched against the raw JSON, a leading `sed -i` sits behind a quote and matches nothing.
func commandStrings(raw json.RawMessage) []string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(v)
	return out
}

// Mark appends what one tool call proved about the turn. It never fails loudly: a hook
// that cannot write must still not break the tool call it follows.
func Mark(dir, sessionID string, marks []string) error {
	if len(marks) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(markerPath(dir, sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(marks, "\n") + "\n")
	return err
}

// Take reads a turn's marks AND consumes them. The two are one operation on purpose: the
// next turn must start from a clean slate, and a marker left behind makes the following
// turn look like it edited code it never touched.
func Take(dir, sessionID string) Marks {
	path := markerPath(dir, sessionID)
	body, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return Marks{}
	}
	m := Marks{Found: true}
	for _, line := range strings.Fields(string(body)) {
		switch line {
		case "edit":
			m.Edited++
		case "ingest":
			m.Recorded = true
		}
	}
	return m
}

// Sweep removes markers left by sessions that ended without an end-of-turn event — a
// crash, a kill. Called from the write path, where the cost is already paid.
func Sweep(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "turn-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func markerPath(dir, sessionID string) string {
	id := unsafeInSessionID.ReplaceAllString(sessionID, "_")
	if id == "" {
		id = "nosession"
	}
	if len(id) > 96 {
		id = id[:96]
	}
	return filepath.Join(dir, "turn-"+id)
}
