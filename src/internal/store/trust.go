package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Trust records which committed <repo>/.kgairc configurations this machine has approved.
//
// The project layer is the one layer a person does not write: it arrives with
// `git clone`, from whoever wrote that repository. Two of its keys used to act on that
// authority alone — `store` chose a directory the engine creates and writes into, and
// the capture rules it carries are injected into every session. So the file decides
// nothing until someone approves it, and a change asks again. This is the direnv model,
// for the same reason direnv uses it.
//
// What is approved is the SETTINGS the file asks for, not its bytes: reformatting it,
// reordering keys or adding a comment changes nothing about what it does, and asking
// again for those only teaches people to accept without reading. A company that
// standardizes one .kgairc across twenty repos is therefore asked once, and every repo
// carrying that same configuration — including ones created later — is covered.
//
// Approvals live in <KgaiHome>/trusted.json, per machine and per user, never synced:
//
//	{"sha256:9f86d0…": {"approved_at": "…", "paths": ["/home/alex/work/shop-api/.kgairc"]}}
//
// `paths` is provenance, not identity: it says where this configuration was accepted
// from, so a repo inheriting an existing approval can say so once instead of appearing
// out of nowhere.
const trustFileName = "trusted.json"

// TrustRecord is one decided configuration. A record is APPROVED when ApprovedAt is set
// (its store and rules take effect) and DISMISSED when only DismissedAt is set (the user
// has seen it and chosen not to approve — the pending prompt stops, but the file still
// decides nothing). Approving a dismissed configuration promotes it and clears the
// dismissal; `kg trust --revoke` deletes the record either way, re-arming the prompt.
type TrustRecord struct {
	ApprovedAt  string   `json:"approved_at,omitempty"`
	DismissedAt string   `json:"dismissed_at,omitempty"`
	Paths       []string `json:"paths"`
}

func trustPath() string { return filepath.Join(KgaiHome(), trustFileName) }

func loadTrust() (map[string]TrustRecord, error) {
	m := map[string]TrustRecord{}
	b, err := os.ReadFile(trustPath())
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", trustPath(), err)
	}
	return m, nil
}

// SettingsFingerprint hashes what a committed config actually ASKS FOR: the values of
// the keys the project layer is allowed to set, canonically ordered. Keys it may not set
// are excluded — they are ignored on read, so they cannot change what approval means.
// A configuration that asks for nothing has no fingerprint and needs no approval.
func SettingsFingerprint(st Settings) string {
	vals := map[string]string{}
	for _, k := range SettingKeys {
		if !keyAllowedIn(k, LayerProject) {
			continue
		}
		if v, _ := st.Get(k); v != "" {
			vals[k] = v
		}
	}
	if len(vals) == 0 {
		return ""
	}
	b, _ := json.Marshal(vals) // encoding/json orders map keys, so this is canonical
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FingerprintOf reads a config file and returns the fingerprint of what it asks for.
func FingerprintOf(path string) (string, error) {
	var st Settings
	var exists bool
	if err := readSettings(path, &st, &exists); err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return SettingsFingerprint(st), nil
}

// TrustState answers "may this config decide anything here?".
type TrustState struct {
	// Trusted is false while the configuration is waiting for a person to accept it.
	Trusted bool
	// Dismissed is true when the user has seen the configuration and chosen not to
	// approve it: it still decides nothing (Trusted stays false), but it is no longer
	// pending — the prompt is silenced. Trusted and Dismissed are never both true.
	Dismissed bool
	// InheritedFrom names the repo this exact configuration was first approved from,
	// when the approval did not come from this path. Empty when it was approved here,
	// or when nothing is approved. Callers announce it once and then Ack it.
	InheritedFrom string
	// Fingerprint of what the file asks for ("" when it asks for nothing).
	Fingerprint string
}

// TrustStateOf reports whether the configuration at path may take effect.
func TrustStateOf(path string) (TrustState, error) {
	fp, err := FingerprintOf(path)
	if err != nil {
		return TrustState{}, err
	}
	if fp == "" {
		return TrustState{Trusted: true}, nil // nothing to approve
	}
	m, err := loadTrust()
	if err != nil {
		return TrustState{}, err
	}
	rec, ok := m[fp]
	if !ok {
		return TrustState{Fingerprint: fp}, nil // pending: not seen, not approved
	}
	if rec.ApprovedAt == "" {
		// Recorded but not approved → dismissed: silence the prompt, decide nothing.
		return TrustState{Dismissed: true, Fingerprint: fp}, nil
	}
	st := TrustState{Trusted: true, Fingerprint: fp}
	abs := absOrSelf(path)
	if !contains(rec.Paths, abs) && len(rec.Paths) > 0 {
		st.InheritedFrom = rec.Paths[0]
	}
	return st, nil
}

// IsTrusted is TrustStateOf reduced to the question most callers ask.
func IsTrusted(path string) (bool, error) {
	st, err := TrustStateOf(path)
	return st.Trusted, err
}

// Trust approves the configuration at path as it currently asks, and returns its
// fingerprint. Approving is per configuration, so every repo carrying the same one is
// covered from here on.
func Trust(path string) (string, error) {
	fp, err := FingerprintOf(path)
	if err != nil {
		return "", err
	}
	if fp == "" {
		return "", fmt.Errorf("%s asks for nothing that needs approval", path)
	}
	return fp, recordPath(fp, absOrSelf(path))
}

// Ack records that this repo is using an already-approved configuration, so the
// inheritance is announced once rather than at every session.
func Ack(path string) error {
	st, err := TrustStateOf(path)
	if err != nil {
		return err
	}
	if !st.Trusted || st.Fingerprint == "" {
		return fmt.Errorf("%s is not approved — nothing to acknowledge", path)
	}
	return recordPath(st.Fingerprint, absOrSelf(path))
}

func recordPath(fp, abs string) error {
	m, err := loadTrust()
	if err != nil {
		return err
	}
	rec := m[fp]
	if rec.ApprovedAt == "" {
		rec.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Approving supersedes any prior dismissal of the same configuration.
	rec.DismissedAt = ""
	if !contains(rec.Paths, abs) {
		rec.Paths = append(rec.Paths, abs)
	}
	m[fp] = rec
	return saveTrust(m)
}

// Dismiss records that the user has seen the configuration at path and chosen not to
// approve it, so the pending prompt stops — without approving it (its store and rules
// still do not apply). Bound to the fingerprint like approval: a later change to the file
// asks again. Returns the fingerprint, or an error if the file asks for nothing.
func Dismiss(path string) (string, error) {
	fp, err := FingerprintOf(path)
	if err != nil {
		return "", err
	}
	if fp == "" {
		return "", fmt.Errorf("%s asks for nothing that needs a decision", path)
	}
	m, err := loadTrust()
	if err != nil {
		return "", err
	}
	rec := m[fp]
	// Approving is strictly stronger; never downgrade an approval to a dismissal.
	if rec.ApprovedAt == "" {
		rec.DismissedAt = time.Now().UTC().Format(time.RFC3339)
	}
	abs := absOrSelf(path)
	if !contains(rec.Paths, abs) {
		rec.Paths = append(rec.Paths, abs)
	}
	m[fp] = rec
	return fp, saveTrust(m)
}

// Untrust withdraws approval for the configuration this path carries — everywhere, since
// approval is per configuration. Reports whether there was one to withdraw.
func Untrust(path string) (bool, error) {
	fp, err := FingerprintOf(path)
	if err != nil {
		return false, err
	}
	m, err := loadTrust()
	if err != nil {
		return false, err
	}
	if _, ok := m[fp]; !ok {
		return false, nil
	}
	delete(m, fp)
	return true, saveTrust(m)
}

// TrustedConfigs lists every configuration on record — approved and dismissed alike — and
// where each was accepted from. Callers that must distinguish the two use DecidedConfigs.
func TrustedConfigs() (map[string]TrustRecord, error) { return loadTrust() }

// DecidedConfigs splits the recorded configurations into approved (ApprovedAt set, their
// store and rules take effect) and dismissed (only DismissedAt set, inert but silenced).
// `kg trust --list` reports them separately, so a dismissed config is never counted or
// labelled as approved.
func DecidedConfigs() (approved, dismissed map[string]TrustRecord, err error) {
	m, err := loadTrust()
	if err != nil {
		return nil, nil, err
	}
	approved, dismissed = map[string]TrustRecord{}, map[string]TrustRecord{}
	for fp, rec := range m {
		if rec.ApprovedAt != "" {
			approved[fp] = rec
		} else {
			dismissed[fp] = rec
		}
	}
	return approved, dismissed, nil
}

func saveTrust(m map[string]TrustRecord) error {
	if err := os.MkdirAll(KgaiHome(), 0o755); err != nil {
		return err
	}
	for fp, rec := range m {
		sort.Strings(rec.Paths)
		m[fp] = rec
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Temp + rename: a half-written trusted.json fails to parse, and every command reads
	// it — a crash mid-write would take out the whole CLI until someone deleted the file.
	tmp := trustPath() + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, trustPath())
}

func absOrSelf(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
