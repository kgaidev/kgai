package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The layered config decides where a project's decisions sync to and which capture
// rules the agent is given, so precedence mistakes either strand a project offline,
// push its log somewhere unintended, or silently drop the team's rules. The contract:
// session (kg.config.json) > project (<repo>/.kgairc) > global (<KGAI_HOME>/config.json),
// every key overrides rather than merges, and the winning layer is always nameable.

// layered pins all three layers into temp dirs so the developer's real config can
// never leak into a test, and returns the store carrying the session layer.
func layered(t *testing.T, session, project, global Settings) *Store {
	t.Helper()
	t.Setenv("KGAI_HOME", t.TempDir())
	proj := t.TempDir()
	t.Setenv("KGAI_PROJECT", proj)
	if project != (Settings{}) {
		writeJSON(t, filepath.Join(proj, ProjectConfigName), project)
		// These tests are about layering, not about the approval gate: approve the
		// file so the layer is live. The gate itself is covered in trust_test.go.
		if _, err := Trust(filepath.Join(proj, ProjectConfigName)); err != nil {
			t.Fatal(err)
		}
	}
	if global != (Settings{}) {
		if err := SaveGlobalConfig(global); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{Root: filepath.Join(proj, ".kgai", "store"), Config: Config{Settings: session}}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := WriteLayer("test", path, "remote", ""); err != nil { // creates the file
		t.Fatal(err)
	}
	s := v.(Settings)
	for k, val := range map[string]string{"remote": s.Remote, "prompt": s.Prompt, "cloud_url": s.CloudURL} {
		if val == "" {
			continue
		}
		if err := WriteLayer("test", path, k, val); err != nil {
			t.Fatal(err)
		}
	}
}

func effectiveOf(t *testing.T, s *Store, key string) (string, string) {
	t.Helper()
	layers, err := LoadLayers(s)
	if err != nil {
		t.Fatal(err)
	}
	return Effective(layers, key)
}

func TestSessionBeatsProjectBeatsGlobal(t *testing.T) {
	s := layered(t,
		Settings{Prompt: "session rules"},
		Settings{Prompt: "repo rules"},
		Settings{Prompt: "my rules"})
	val, src := effectiveOf(t, s, "prompt")
	if val != "session rules" || src != LayerSession {
		t.Errorf("got (%q, %q), want the session layer to win", val, src)
	}
}

func TestProjectBeatsGlobalWhenSessionIsSilent(t *testing.T) {
	s := layered(t, Settings{}, Settings{Prompt: "repo rules"}, Settings{Prompt: "my rules"})
	val, src := effectiveOf(t, s, "prompt")
	if val != "repo rules" || src != LayerProject {
		t.Errorf("got (%q, %q), want the committed repo layer to win over the machine default", val, src)
	}
}

func TestGlobalIsTheFallback(t *testing.T) {
	s := layered(t, Settings{}, Settings{}, Settings{Prompt: "my rules"})
	val, src := effectiveOf(t, s, "prompt")
	if val != "my rules" || src != LayerGlobal {
		t.Errorf("got (%q, %q), want the global fallback", val, src)
	}
}

// A prompt OVERRIDES, it does not accumulate: whoever wins owns the whole text, so the
// agent is never handed two rule sets that contradict each other.
func TestPromptOverridesRatherThanMerges(t *testing.T) {
	s := layered(t, Settings{}, Settings{Prompt: "repo rules"}, Settings{Prompt: "my rules"})
	val, _ := effectiveOf(t, s, "prompt")
	if val != "repo rules" {
		t.Errorf("got %q, want only the winning layer's text", val)
	}
}

// Keys resolve independently — a project that only sets a prompt must not shadow the
// remote configured globally.
func TestKeysResolveIndependently(t *testing.T) {
	s := layered(t, Settings{}, Settings{Prompt: "repo rules"}, Settings{Remote: "s3://b/p"})
	if val, src := effectiveOf(t, s, "prompt"); val != "repo rules" || src != LayerProject {
		t.Errorf("prompt: got (%q, %q)", val, src)
	}
	if val, src := effectiveOf(t, s, "remote"); val != "s3://b/p" || src != LayerGlobal {
		t.Errorf("remote: got (%q, %q)", val, src)
	}
}

func TestUnsetEverywhere(t *testing.T) {
	s := layered(t, Settings{}, Settings{}, Settings{})
	if val, src := effectiveOf(t, s, "prompt"); val != "" || src != "" {
		t.Errorf("got (%q, %q), want empty — nothing configured anywhere", val, src)
	}
}

// Configuration must be readable before `kg init` — a hook asks for the prompt at
// session start, when a fresh clone has no store at all.
func TestLayersWithoutAStore(t *testing.T) {
	layered(t, Settings{}, Settings{Prompt: "repo rules"}, Settings{})
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, src := Effective(layers, "prompt"); val != "repo rules" || src != LayerProject {
		t.Errorf("got (%q, %q) with no store, want the repo layer", val, src)
	}
	if layers[0].Name != LayerSession || layers[0].Exists {
		t.Errorf("session layer should be present but empty, got %+v", layers[0])
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	var st Settings
	if err := st.Set("promt", "x"); err == nil {
		t.Fatal("a typo'd key must fail loudly, not be stored and ignored")
	}
	if _, err := st.Get("promt"); err == nil {
		t.Fatal("reading an unknown key must be an error, not an empty value")
	}
}

func TestPromptIsCapped(t *testing.T) {
	var st Settings
	if err := st.Set("prompt", string(make([]byte, PromptMaxBytes+1))); err == nil {
		t.Fatalf("a prompt over %d bytes must be refused — it is injected into every session", PromptMaxBytes)
	}
}

// A corrupt layer must not read as "unset": that would silently push decisions to a
// different remote, or drop the rules the team relies on.
func TestCorruptLayerIsAnError(t *testing.T) {
	s := layered(t, Settings{}, Settings{}, Settings{})
	if err := os.WriteFile(ProjectConfigPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayers(s); err == nil {
		t.Fatal("corrupt .kgairc must be reported, not treated as empty")
	}
}

// The repo layer is found from a subdirectory the way npm finds .npmrc: walk up, but
// never past the project root.
func TestProjectConfigFoundFromSubdirectory(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ProjectConfigName), []byte(`{"prompt":"repo rules"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if got := ProjectConfigPath(); got != filepath.Join(repo, ProjectConfigName) {
		t.Errorf("got %q, want the repo-root .kgairc found by walking up", got)
	}
}

// With no file anywhere, the path reported is where one WOULD be written, so
// `kg config --project` has a destination and the answer is never empty.
func TestProjectConfigPathDefaultsToRepoRoot(t *testing.T) {
	repo := newRepo(t)
	t.Chdir(repo)
	if got := ProjectConfigPath(); got != filepath.Join(repo, ProjectConfigName) {
		t.Errorf("got %q, want %q", got, filepath.Join(repo, ProjectConfigName))
	}
}
