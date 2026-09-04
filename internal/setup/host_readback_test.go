package setup

import "testing"

func TestCodexInstalledPluginReadbackIgnoresAvailableCatalog(t *testing.T) {
	plugin, err := parsePlugin([]byte(`{"available":[{"name":"jetkvm","version":"9.0.0","marketplaceName":"foreign"}],"installed":[{"pluginId":"jetkvm@jetkvm","name":"jetkvm","version":"1.0.0","marketplaceName":"jetkvm"}]}`))
	if err != nil || !plugin.Present || plugin.Version != "1.0.0" || plugin.Source != "jetkvm" {
		t.Fatalf("plugin=%+v error=%v", plugin, err)
	}
	transport, err := findJSONComponent([]byte(`{"name":"jetkvm","transport":{"type":"stdio","command":"jetkvm","args":["mcp","serve","--transport=stdio"]}}`), []string{"jetkvm"}, "command")
	if err != nil || !equivalentMCP(transport) {
		t.Fatalf("transport=%+v error=%v", transport, err)
	}
	snapshot := Snapshot{
		Target:      Target{Host: HostCodex, Mode: ModePlugin, Scope: ScopeUser},
		Marketplace: Component{Present: true, Source: MarketplaceSource},
		Plugin:      plugin, DirectMCP: transport,
	}
	if got := classify(snapshot, "1.0.0"); got != StateEquivalent {
		t.Fatalf("plugin-provided MCP classified as %s", got)
	}
	snapshot.DirectMCP.Command = "different-server"
	if got := classify(snapshot, "1.0.0"); got != StateForeignConflict {
		t.Fatalf("conflicting MCP classified as %s", got)
	}
}

func TestAvailablePluginIsNotAnInstalledPlugin(t *testing.T) {
	plugin, err := parsePlugin([]byte(`{"installed":[],"available":[{"name":"jetkvm","version":"1.0.0","marketplaceName":"jetkvm"}]}`))
	if err != nil || plugin.Present {
		t.Fatalf("plugin=%+v error=%v", plugin, err)
	}
}
