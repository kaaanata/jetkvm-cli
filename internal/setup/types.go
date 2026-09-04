// Package setup owns the transactional domain core for installing JetKVM into
// supported agent hosts. Host-native CLIs remain the terminal authority.
package setup

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"uuid"
)

const (
	CanonicalMCPCommand = "jetkvm"
	MarketplaceSource   = "kaaanata/jetkvm-cli"
	MarketplaceName     = "jetkvm"
	PluginReference     = "jetkvm@jetkvm"
)

var (
	ErrInvalidTarget    = errors.New("invalid setup target")
	ErrHostUnavailable  = errors.New("agent host CLI is unavailable")
	ErrCommandFailed    = errors.New("agent host command failed")
	ErrSetupConflict    = errors.New("setup conflict")
	ErrMigrationNeeded  = errors.New("legacy direct integration requires explicit migration")
	ErrInvalidPlan      = errors.New("setup plan does not match the authoritative transition")
	ErrStalePlan        = errors.New("setup plan is stale")
	ErrReceiptNotFound  = errors.New("setup receipt not found")
	ErrRollbackConflict = errors.New("setup rollback ownership conflict")
	ErrVerification     = errors.New("setup verification failed")
)

type Host string

const (
	HostCodex      Host = "codex"
	HostClaudeCode Host = "claude-code"
)

type Mode string

const (
	ModePlugin Mode = "plugin"
	ModeDirect Mode = "direct"
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeLocal   Scope = "local"
)

type State string

const (
	StateAbsent          State = "absent"
	StateEquivalent      State = "equivalent"
	StateOwnedOutdated   State = "owned_outdated"
	StateForeignConflict State = "foreign_conflict"
	StateLegacyDirect    State = "legacy_direct"
	StatePartial         State = "partial"
)

type Target struct {
	Host      Host   `json:"host"`
	Mode      Mode   `json:"mode"`
	Scope     Scope  `json:"scope"`
	Workspace string `json:"workspace,omitempty"`
}

// Normalize applies the public setup defaults. Direct mode is never inferred:
// only an explicit ModeDirect value selects it.
func (t Target) Normalize() Target {
	if t.Mode == "" {
		t.Mode = ModePlugin
	}
	if t.Scope == "" {
		t.Scope = ScopeUser
	}
	return t
}

func (t Target) Validate() error {
	t = t.Normalize()
	if t.Host != HostCodex && t.Host != HostClaudeCode {
		return fmt.Errorf("%w: unsupported host %q", ErrInvalidTarget, t.Host)
	}
	if t.Mode != ModePlugin && t.Mode != ModeDirect {
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidTarget, t.Mode)
	}
	if t.Scope != ScopeUser && t.Scope != ScopeProject && t.Scope != ScopeLocal {
		return fmt.Errorf("%w: unsupported scope %q", ErrInvalidTarget, t.Scope)
	}
	if t.Host == HostCodex && t.Scope != ScopeUser {
		return fmt.Errorf("%w: Codex host-native setup supports user scope only", ErrInvalidTarget)
	}
	if t.Scope == ScopeUser && t.Workspace != "" {
		return fmt.Errorf("%w: user scope must not specify a workspace", ErrInvalidTarget)
	}
	if t.Scope != ScopeUser && (t.Workspace == "" || !filepath.IsAbs(t.Workspace)) {
		return fmt.Errorf("%w: project and local scopes require an absolute workspace", ErrInvalidTarget)
	}
	return nil
}

type Component struct {
	Present bool     `json:"present"`
	Source  string   `json:"source,omitempty"`
	Version string   `json:"version,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type Snapshot struct {
	Target      Target    `json:"target"`
	HostVersion string    `json:"host_version,omitempty"`
	Marketplace Component `json:"marketplace"`
	Plugin      Component `json:"plugin"`
	DirectMCP   Component `json:"direct_mcp"`
	State       State     `json:"state"`
	Fingerprint string    `json:"fingerprint"`
}

type Command struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Step struct {
	Name      string  `json:"name"`
	Do        Command `json:"do"`
	Undo      Command `json:"undo"`
	Component string  `json:"component"`
	Creates   bool    `json:"creates"`
}

type PlanRequest struct {
	Target        Target
	PluginVersion string
	Migrate       bool
	DryRun        bool
}

type Plan struct {
	ID                uuid.UUID `json:"id"`
	Target            Target    `json:"target"`
	PluginVersion     string    `json:"plugin_version"`
	InitialState      State     `json:"initial_state"`
	BeforeFingerprint string    `json:"before_fingerprint"`
	Steps             []Step    `json:"steps,omitempty"`
	DryRun            bool      `json:"dry_run"`
	CreatedAt         time.Time `json:"created_at"`
}

type ReceiptStatus string

const (
	ReceiptPrepared         ReceiptStatus = "prepared"
	ReceiptCommitted        ReceiptStatus = "committed"
	ReceiptRolledBack       ReceiptStatus = "rolled_back"
	ReceiptRollbackConflict ReceiptStatus = "rollback_conflict"
	ReceiptFailed           ReceiptStatus = "failed"
	ReceiptDryRun           ReceiptStatus = "dry_run"
	ReceiptUninstalled      ReceiptStatus = "uninstalled"
)

type JournalEntry struct {
	Step             string    `json:"step"`
	Command          Command   `json:"command"`
	AfterFingerprint string    `json:"after_fingerprint,omitempty"`
	CompletedAt      time.Time `json:"completed_at"`
}

type Receipt struct {
	SchemaVersion     int            `json:"schema_version"`
	ID                uuid.UUID      `json:"id"`
	PlanID            uuid.UUID      `json:"plan_id"`
	Target            Target         `json:"target"`
	PluginVersion     string         `json:"plugin_version,omitempty"`
	Status            ReceiptStatus  `json:"status"`
	InitialState      State          `json:"initial_state"`
	BeforeFingerprint string         `json:"before_fingerprint"`
	AfterFingerprint  string         `json:"after_fingerprint,omitempty"`
	OwnedComponents   []string       `json:"owned_components,omitempty"`
	Journal           []JournalEntry `json:"journal,omitempty"`
	RollbackJournal   []JournalEntry `json:"rollback_journal,omitempty"`
	FailureKind       string         `json:"failure_kind,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type DoctorStatus string

const (
	DoctorReady          DoctorStatus = "ready"
	DoctorActionRequired DoctorStatus = "action_required"
	DoctorConflict       DoctorStatus = "conflict"
)

type DoctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DoctorReport struct {
	Target   Target        `json:"target"`
	Status   DoctorStatus  `json:"status"`
	State    State         `json:"state"`
	Checks   []DoctorCheck `json:"checks"`
	Observed time.Time     `json:"observed_at"`
}

func canonicalMCPArgs() []string {
	return []string{"mcp", "serve", "--transport=stdio"}
}
