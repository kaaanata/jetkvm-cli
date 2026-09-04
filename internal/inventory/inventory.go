package inventory

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

type EffectClass = domain.EffectClass

const (
	EffectObserve      = domain.EffectObserve
	EffectInput        = domain.EffectInput
	EffectPower        = domain.EffectPower
	EffectMedia        = domain.EffectMedia
	EffectAdmin        = domain.EffectAdmin
	EffectIrreversible = domain.EffectIrreversible
)

type RetryPolicy string

const (
	RetryBoundedRead    RetryPolicy = "bounded_read"
	RetryNeverAfterSend RetryPolicy = "never_after_send"
)

type ReceiptSemantics string

const (
	ReceiptObservation ReceiptSemantics = "observation"
	ReceiptOperation   ReceiptSemantics = "operation"
)

// ToolDefinition is static metadata. Runtime credentials, connections and
// authorization state must never be captured here.
type ToolDefinition struct {
	Name         string
	Toolset      string
	Effect       EffectClass
	ReadOnly     bool
	Destructive  bool
	Idempotent   bool
	OpenWorld    bool
	DeviceScoped bool
	Capability   string
	InputSchema  string
	OutputSchema string
	Retry        RetryPolicy
	Receipt      ReceiptSemantics
}

type Catalog struct {
	tools map[string]ToolDefinition
}

func New(definitions []ToolDefinition) (Catalog, error) {
	tools := make(map[string]ToolDefinition, len(definitions))
	var errs []error
	for _, definition := range definitions {
		if err := definition.validate(); err != nil {
			errs = append(errs, fmt.Errorf("tool %q: %w", definition.Name, err))
			continue
		}
		if _, exists := tools[definition.Name]; exists {
			errs = append(errs, fmt.Errorf("duplicate tool %q", definition.Name))
			continue
		}
		tools[definition.Name] = definition
	}
	if err := errors.Join(errs...); err != nil {
		return Catalog{}, err
	}
	return Catalog{tools: tools}, nil
}

func (c Catalog) Get(name string) (ToolDefinition, bool) {
	definition, ok := c.tools[name]
	return definition, ok
}

func (c Catalog) All() []ToolDefinition {
	definitions := slices.Collect(maps.Values(c.tools))
	slices.SortFunc(definitions, func(a, b ToolDefinition) int {
		if a.Toolset == b.Toolset {
			return compare(a.Name, b.Name)
		}
		return compare(a.Toolset, b.Toolset)
	})
	return definitions
}

func (d ToolDefinition) validate() error {
	var errs []error
	if d.Name == "" || d.Toolset == "" || d.InputSchema == "" || d.OutputSchema == "" {
		errs = append(errs, errors.New("name, toolset, input schema and output schema are required"))
	}
	switch d.Effect {
	case EffectObserve, EffectInput, EffectPower, EffectMedia, EffectAdmin, EffectIrreversible:
	default:
		errs = append(errs, fmt.Errorf("unknown effect class %q", d.Effect))
	}
	if d.ReadOnly && d.Receipt != ReceiptObservation {
		errs = append(errs, errors.New("read-only tools require observation receipts"))
	}
	if !d.ReadOnly && d.Receipt != ReceiptOperation {
		errs = append(errs, errors.New("state-changing tools require operation receipts"))
	}
	if d.ReadOnly && d.Retry != RetryBoundedRead {
		errs = append(errs, errors.New("read-only tools require bounded-read retry policy"))
	}
	if !d.ReadOnly && d.Retry != RetryNeverAfterSend {
		errs = append(errs, errors.New("state-changing tools must never retry after send"))
	}
	if d.Effect != EffectObserve && d.ReadOnly {
		errs = append(errs, errors.New("non-observe effect cannot be read-only"))
	}
	return errors.Join(errs...)
}

func compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
