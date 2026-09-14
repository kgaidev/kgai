package turn

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The payloads are real ones from each host, not invented shapes: what this package is
// for is surviving the differences between them, so a test written against a made-up
// payload would prove nothing.
func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{"claude edit tool", `{"tool_name":"Edit","tool_input":{"file_path":"/p/a.js","old_string":"a","new_string":"b"}}`, []string{"edit"}},
		{"claude write tool", `{"tool_name":"Write","tool_input":{"file_path":"/p/a.js","content":"x"}}`, []string{"edit"}},
		{"codex apply_patch", `{"tool_name":"apply_patch","tool_input":{"input":"*** Update File: src/a.js"}}`, []string{"edit"}},
		{"gemini write_file", `{"tool_name":"write_file","tool_input":{"file_path":"/p/a.js","content":"x"}}`, []string{"edit"}},
		{"gemini replace", `{"tool_name":"replace","tool_input":{"file_path":"/p/a.js"}}`, []string{"edit"}},

		// Codex and Gemini edit through the shell as often as through their edit tool, and
		// so does Claude Code when the harness asks for bash-first work. Before these were
		// counted, such a turn looked like it had edited nothing and was never nudged.
		{"heredoc into a file", `{"tool_name":"Bash","tool_input":{"command":"cat > src/a.js <<'JS'\nx\nJS"}}`, []string{"edit"}},
		{"in-place sed", `{"tool_name":"run_shell_command","tool_input":{"command":"sed -i s/a/b/ src/a.js"}}`, []string{"edit"}},
		{"git apply", `{"tool_name":"shell","tool_input":{"command":["bash","-lc","git apply /tmp/p.diff"]}}`, []string{"edit"}},
		// Codex 0.154's shell tool is named `exec`; an in-place edit through it still counts.
		{"codex exec sed -i", `{"tool_name":"exec","tool_input":{"command":["/bin/bash","-lc","sed -i s/a/b/ src/a.js"]}}`, []string{"edit"}},
		{"codex exec read-only is not an edit", `{"tool_name":"exec","tool_input":{"command":["/bin/bash","-lc","sed -n 1,3p src/a.js"]}}`, nil},

		{"recording a decision", `{"tool_name":"Bash","tool_input":{"command":"kg ingest < payload.json"}}`, []string{"ingest"}},
		{"recording via the command", `{"tool_name":"Bash","tool_input":{"command":"kg-decision"}}`, []string{"ingest"}},

		// `>/dev/null` is how a shell command silences itself, not how it writes code.
		{"silencing redirect", `{"tool_name":"Bash","tool_input":{"command":"kg status >/dev/null 2>&1"}}`, nil},
		{"appending to /dev/null", `{"tool_name":"Bash","tool_input":{"command":"make >> /dev/null"}}`, nil},
		{"reading a file", `{"tool_name":"Bash","tool_input":{"command":"cat src/a.js"}}`, nil},
		{"unrelated tool", `{"tool_name":"read_file","tool_input":{"path":"/p/a.js"}}`, nil},
		{"no tool at all", `{"session_id":"s"}`, nil},
		{"not json", `nonsense`, nil},

		// The regression this pins: `kg ingest` appears verbatim in this plugin's own skill
		// file. Reading that file is not recording a decision, and counting it as one would
		// silence the capture nudge for the rest of the turn — the exact failure the whole
		// mechanism exists to prevent.
		{
			"kg ingest in tool OUTPUT is not a recording",
			`{"tool_name":"Bash","tool_input":{"command":"cat SKILL.md"},"tool_response":{"output":"run kg ingest to record"}}`,
			nil,
		},
		// One command can do both, and the turn owes nothing extra for the edit.
		{
			"recording and editing at once",
			`{"tool_name":"Bash","tool_input":{"command":"kg ingest < p.json > /tmp/receipt.txt"}}`,
			[]string{"ingest", "edit"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify([]byte(c.payload))
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Classify(%s) = %v, want %v", c.payload, got, c.want)
			}
		})
	}
}

func TestMarkAndTake(t *testing.T) {
	dir := t.TempDir()
	if err := Mark(dir, "s1", []string{"edit"}); err != nil {
		t.Fatal(err)
	}
	if err := Mark(dir, "s1", []string{"edit"}); err != nil {
		t.Fatal(err)
	}
	got := Take(dir, "s1")
	if !got.Found || got.Edited != 2 || got.Recorded {
		t.Fatalf("Take = %+v, want 2 edits, not recorded, found", got)
	}
}

// Take consumes. Without that, the turn after a nudge inherits the previous turn's edits
// and is told it changed code it never touched.
func TestTakeConsumes(t *testing.T) {
	dir := t.TempDir()
	Mark(dir, "s1", []string{"edit"})
	if first := Take(dir, "s1"); !first.Found {
		t.Fatal("first Take found nothing")
	}
	if second := Take(dir, "s1"); second.Found || second.Edited != 0 {
		t.Fatalf("second Take = %+v, want an empty, not-found result", second)
	}
}

// "Nothing was marked" and "the turn edited nothing" are different answers: the first
// means this mechanism never ran and the caller should fall back, the second is a verdict.
func TestTakeDistinguishesMissingFromEmpty(t *testing.T) {
	dir := t.TempDir()
	if m := Take(dir, "never-seen"); m.Found {
		t.Fatalf("Take on an unknown session reported Found")
	}
	Mark(dir, "s1", []string{"ingest"})
	m := Take(dir, "s1")
	if !m.Found || m.Edited != 0 || !m.Recorded {
		t.Fatalf("Take = %+v, want found, 0 edits, recorded", m)
	}
}

func TestSessionsAreSeparate(t *testing.T) {
	dir := t.TempDir()
	Mark(dir, "a", []string{"edit"})
	Mark(dir, "b", []string{"ingest"})
	if a := Take(dir, "a"); a.Edited != 1 || a.Recorded {
		t.Fatalf("session a = %+v", a)
	}
	if b := Take(dir, "b"); b.Edited != 0 || !b.Recorded {
		t.Fatalf("session b = %+v", b)
	}
}

// The session id comes from the host. A traversal in it must not let a marker be written
// outside the state directory.
func TestSessionIDCannotEscapeTheStateDir(t *testing.T) {
	dir := t.TempDir()
	if err := Mark(dir, "../../etc/passwd", []string{"edit"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one marker in the state dir, got %d", len(entries))
	}
	if filepath.Dir(filepath.Join(dir, entries[0].Name())) != dir {
		t.Fatalf("marker %q escaped %q", entries[0].Name(), dir)
	}
}

func TestSweepRemovesOnlyStaleMarkers(t *testing.T) {
	dir := t.TempDir()
	Mark(dir, "old", []string{"edit"})
	Mark(dir, "fresh", []string{"edit"})
	stale := filepath.Join(dir, "turn-old")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	Sweep(dir, 24*time.Hour)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a day-old marker survived the sweep")
	}
	if m := Take(dir, "fresh"); !m.Found {
		t.Fatal("the sweep took a marker from a live session")
	}
}

// Every call site is a hook that runs after a tool call the user is waiting on. Failing
// loudly there would break the tool call; failing silently only costs one nudge.
func TestMarkOnAnUnwritableDirIsSurvivable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root walks straight through the permission bits")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := Mark(filepath.Join(dir, "state"), "s", []string{"edit"}); err == nil {
		t.Fatal("expected an error the caller can ignore, got nil")
	}
	// Take must still answer, so the end-of-turn hook can fall back rather than crash.
	if m := Take(filepath.Join(dir, "state"), "s"); m.Found {
		t.Fatalf("Take = %+v, want not found", m)
	}
}
