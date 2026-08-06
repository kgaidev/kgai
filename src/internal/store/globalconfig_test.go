package store

import (
	"os"
	"path/filepath"
	"testing"
)

// EffectiveRemote decides where a project's decisions sync to, so precedence mistakes
// either strand a project offline or push its log somewhere unintended. The contract:
// session kg.config.json > project .kgairc > global <KGAI_HOME>/config.json, and the
// sentinel "none" in a non-global layer opts a project out entirely.

// testStore gives a Store with the given local remote, a temp KGAI_HOME (so the real
// global config never leaks in), and a fixed project root for {project} expansion.
func testStore(t *testing.T, localRemote string) *Store {
	t.Helper()
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("KGAI_PROJECT", filepath.Join(string(os.PathSeparator), "work", "shop-api"))
	return &Store{Config: Config{Settings: Settings{Remote: localRemote}}}
}

func setGlobal(t *testing.T, remote string) {
	t.Helper()
	if err := SaveGlobalConfig(Settings{Remote: remote}); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveRemoteSessionWinsOverGlobal(t *testing.T) {
	s := testStore(t, "s3://local-bucket/kg")
	setGlobal(t, "s3://global-bucket/kg/{project}")
	url, source := s.EffectiveRemote()
	if url != "s3://local-bucket/kg" || source != LayerSession {
		t.Errorf("got (%q, %q), want the session remote to win", url, source)
	}
}

func TestEffectiveRemoteFallsBackToGlobal(t *testing.T) {
	s := testStore(t, "")
	setGlobal(t, "s3://global-bucket/kg/{project}")
	url, source := s.EffectiveRemote()
	if url != "s3://global-bucket/kg/shop-api" || source != "global" {
		t.Errorf("got (%q, %q), want the global remote with {project} expanded", url, source)
	}
}

func TestEffectiveRemoteGlobalWithoutPlaceholderIsVerbatim(t *testing.T) {
	s := testStore(t, "")
	setGlobal(t, "s3://team-bucket/one-shared-graph")
	url, source := s.EffectiveRemote()
	if url != "s3://team-bucket/one-shared-graph" || source != "global" {
		t.Errorf("got (%q, %q), want the global remote verbatim (shared-graph mode)", url, source)
	}
}

func TestEffectiveRemoteNoneOptsOutOfGlobal(t *testing.T) {
	s := testStore(t, RemoteNone)
	setGlobal(t, "s3://global-bucket/kg/{project}")
	url, source := s.EffectiveRemote()
	if url != "" || source != "disabled" {
		t.Errorf("got (%q, %q), want (\"\", \"disabled\") — local %q must beat the global", url, source, RemoteNone)
	}
}

func TestEffectiveRemoteNothingConfigured(t *testing.T) {
	s := testStore(t, "")
	url, source := s.EffectiveRemote()
	if url != "" || source != "" {
		t.Errorf("got (%q, %q), want empty — no remote anywhere", url, source)
	}
}

func TestGlobalConfigRoundTripAndMissingFile(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	gc, err := LoadGlobalConfig()
	if err != nil || gc.Remote != "" {
		t.Fatalf("missing file should be an empty config, got (%+v, %v)", gc, err)
	}
	setGlobal(t, "s3://b/p")
	gc, err = LoadGlobalConfig()
	if err != nil || gc.Remote != "s3://b/p" {
		t.Fatalf("round trip failed: (%+v, %v)", gc, err)
	}
}
