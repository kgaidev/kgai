package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The global layer is <KgaiHome>/config.json: defaults that apply to every project on
// this machine. It is written only when explicitly asked for (`kg config --global`,
// `kg remote --global`) and is the weakest layer — a project's committed .kgairc and
// this install's own kg.config.json both override it. See settings.go for the shape.

// RemoteNone is the sentinel that opts a project out of syncing even when a broader
// layer configures a remote ("this project stays local").
const RemoteNone = "none"

func globalConfigPath() string { return filepath.Join(KgaiHome(), "config.json") }

// GlobalConfigPath is the machine-wide layer's file, exported so writes can go through
// WriteLayer — which merges into what the file already holds instead of replacing it.
func GlobalConfigPath() string { return globalConfigPath() }

// LoadGlobalConfig reads <KgaiHome>/config.json. A missing file is an empty config.
func LoadGlobalConfig() (Settings, error) {
	var gc Settings
	b, err := os.ReadFile(globalConfigPath())
	if os.IsNotExist(err) {
		return gc, nil
	}
	if err != nil {
		return gc, err
	}
	if err := json.Unmarshal(b, &gc); err != nil {
		return gc, fmt.Errorf("corrupt %s: %w", globalConfigPath(), err)
	}
	return gc, nil
}

// SaveGlobalConfig writes <KgaiHome>/config.json, creating the home dir if needed.
func SaveGlobalConfig(gc Settings) error {
	if err := os.MkdirAll(KgaiHome(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(gc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(globalConfigPath(), append(b, '\n'), 0o644)
}

// EffectiveRemote resolves the sync remote this store actually uses and where it came
// from: a layer name (session/project/global), "disabled" when the winning layer set
// the RemoteNone sentinel, or "" when nothing configured one anywhere. The receiver
// may be nil — asking where a project WOULD sync must not require an initialized
// store, let alone create one.
func (s *Store) EffectiveRemote() (url, source string) {
	layers, err := LoadLayers(s)
	if err != nil {
		return "", ""
	}
	val, src := Effective(layers, "remote")
	switch val {
	case "":
		return "", ""
	case RemoteNone:
		return "", "disabled"
	}
	return ExpandRemote(val), src
}

// ExpandRemote fills the {project} placeholder with the project directory's name, so
// one configured remote can give every project its own prefix.
func ExpandRemote(remote string) string {
	return strings.ReplaceAll(remote, "{project}", filepath.Base(ProjectRoot()))
}
