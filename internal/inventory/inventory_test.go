package inventory

import "testing"

func TestStaticCatalogIsValidAndDeterministic(t *testing.T) {
	catalog := Static()
	tools := catalog.All()
	if len(tools) < 15 {
		t.Fatalf("static catalog has only %d tools", len(tools))
	}
	for index := 1; index < len(tools); index++ {
		previous, current := tools[index-1], tools[index]
		if previous.Toolset > current.Toolset || previous.Toolset == current.Toolset && previous.Name >= current.Name {
			t.Fatalf("catalog order is not deterministic at %q and %q", previous.Name, current.Name)
		}
	}
	for _, tool := range tools {
		if tool.Effect == "" || tool.Retry == "" || tool.Receipt == "" || tool.InputSchema == "" || tool.OutputSchema == "" {
			t.Fatalf("tool %q has incomplete classification", tool.Name)
		}
	}
}

func TestNewRejectsMissingClassificationAndUnsafeRetry(t *testing.T) {
	base := ToolDefinition{
		Name: "test", Toolset: "input", Effect: EffectInput,
		ReadOnly: false, InputSchema: "in", OutputSchema: "out",
		Retry: RetryNeverAfterSend, Receipt: ReceiptOperation,
	}
	for name, mutate := range map[string]func(*ToolDefinition){
		"effect": func(d *ToolDefinition) { d.Effect = "" },
		"retry":  func(d *ToolDefinition) { d.Retry = RetryBoundedRead },
		"schema": func(d *ToolDefinition) { d.OutputSchema = "" },
	} {
		t.Run(name, func(t *testing.T) {
			definition := base
			mutate(&definition)
			if _, err := New([]ToolDefinition{definition}); err == nil {
				t.Fatal("New succeeded")
			}
		})
	}
}

func TestNewRejectsDuplicateTool(t *testing.T) {
	definition := observeTool("duplicate", false, "in", "out")
	if _, err := New([]ToolDefinition{definition, definition}); err == nil {
		t.Fatal("New succeeded")
	}
}
