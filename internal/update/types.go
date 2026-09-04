package update

import "time"

const Repository = "kaaanata/jetkvm-cli"

type Owner string

const (
	OwnerStandalone Owner = "standalone"
	OwnerHomebrew   Owner = "homebrew"
	OwnerWinget     Owner = "winget"
	OwnerScoop      Owner = "scoop"
	OwnerDeb        Owner = "deb"
	OwnerRPM        Owner = "rpm"
	OwnerSource     Owner = "source"
	OwnerUnmanaged  Owner = "unmanaged"
	OwnerUnknown    Owner = "unknown"
)

func (o Owner) Valid() bool {
	switch o {
	case OwnerStandalone, OwnerHomebrew, OwnerWinget, OwnerScoop, OwnerDeb, OwnerRPM, OwnerSource, OwnerUnmanaged, OwnerUnknown:
		return true
	default:
		return false
	}
}

type Channel string

const (
	ChannelStable     Channel = "stable"
	ChannelPrerelease Channel = "prerelease"
)

type Request struct {
	Version        string  `json:"version,omitempty"`
	Channel        Channel `json:"channel"`
	AllowDowngrade bool    `json:"allow_downgrade,omitzero"`
}

type Installation struct {
	InstallID   string    `json:"install_id,omitempty"`
	Owner       Owner     `json:"owner"`
	Executable  string    `json:"executable"`
	Version     string    `json:"version"`
	Repository  string    `json:"repository"`
	Channel     Channel   `json:"channel"`
	InstalledAt time.Time `json:"installed_at,omitzero"`
}

type Resolution struct {
	Installation Installation `json:"installation"`
	Request      Request      `json:"request"`
}

type Release struct {
	Version     string    `json:"version"`
	Prerelease  bool      `json:"prerelease,omitzero"`
	AssetName   string    `json:"asset_name"`
	AssetURL    string    `json:"asset_url"`
	PublishedAt time.Time `json:"published_at,omitzero"`
	native      any
}

type CheckResult struct {
	Installation Installation `json:"installation"`
	Request      Request      `json:"request"`
	Release      Release      `json:"release"`
	Available    bool         `json:"available"`
	Downgrade    bool         `json:"downgrade,omitzero"`
}

type PlanAction string

const (
	ActionNone        PlanAction = "none"
	ActionSelfReplace PlanAction = "self_replace"
	ActionRequired    PlanAction = "action_required"
)

type Plan struct {
	Action         PlanAction `json:"action"`
	Owner          Owner      `json:"owner"`
	CurrentVersion string     `json:"current_version"`
	TargetVersion  string     `json:"target_version"`
	Executable     string     `json:"executable"`
	Channel        Channel    `json:"channel"`
	Command        []string   `json:"command,omitempty"`
	Release        Release    `json:"release"`
	InstallID      string     `json:"install_id,omitempty"`
}

type ResultStatus string

const (
	StatusUpToDate       ResultStatus = "up_to_date"
	StatusActionRequired ResultStatus = "action_required"
	StatusApplied        ResultStatus = "applied"
	StatusRolledBack     ResultStatus = "rolled_back"
)

type Result struct {
	Status            ResultStatus `json:"status"`
	Owner             Owner        `json:"owner"`
	PreviousVersion   string       `json:"previous_version,omitempty"`
	CurrentVersion    string       `json:"current_version"`
	Verified          bool         `json:"verified,omitzero"`
	RollbackAvailable bool         `json:"rollback_available,omitzero"`
	ActionRequired    []string     `json:"action_required,omitempty"`
}
