package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

func (s *Service) Inspect(ctx context.Context, target Target, pluginVersion string) (Snapshot, error) {
	target = target.Normalize()
	if err := target.Validate(); err != nil {
		return Snapshot{}, err
	}
	if target.Mode == ModePlugin && strings.TrimSpace(pluginVersion) == "" {
		return Snapshot{}, errors.New("plugin version is required")
	}

	hostVersion, err := s.required(ctx, Command{Name: hostBinary(target.Host), Args: []string{"--version"}, Dir: target.Workspace})
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect %s version: %w", target.Host, err)
	}
	marketplace, err := s.inspectMarketplace(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}
	plugin, err := s.inspectPlugin(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}
	direct, err := s.inspectDirect(ctx, target)
	if err != nil {
		return Snapshot{}, err
	}

	snapshot := Snapshot{
		Target: target, HostVersion: strings.TrimSpace(string(hostVersion.Stdout)),
		Marketplace: marketplace, Plugin: plugin, DirectMCP: direct,
	}
	snapshot.State = classify(snapshot, pluginVersion)
	snapshot.Fingerprint, err = fingerprint(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) inspectMarketplace(ctx context.Context, target Target) (Component, error) {
	command := Command{Name: hostBinary(target.Host), Args: []string{"plugin", "marketplace", "list", "--json"}, Dir: target.Workspace}
	result, err := s.required(ctx, command)
	if err != nil {
		return Component{}, fmt.Errorf("inspect marketplace: %w", err)
	}
	return parseMarketplace(result.Stdout)
}

func (s *Service) inspectPlugin(ctx context.Context, target Target) (Component, error) {
	command := Command{Name: hostBinary(target.Host), Args: []string{"plugin", "list", "--json"}, Dir: target.Workspace}
	result, err := s.required(ctx, command)
	if err != nil {
		return Component{}, fmt.Errorf("inspect plugin: %w", err)
	}
	return parsePlugin(result.Stdout)
}

func (s *Service) inspectDirect(ctx context.Context, target Target) (Component, error) {
	args := []string{"mcp", "get", "jetkvm"}
	if target.Host == HostCodex {
		args = append(args, "--json")
	}
	result, err := s.runner.Run(ctx, Command{Name: hostBinary(target.Host), Args: args, Dir: target.Workspace})
	if err != nil {
		return Component{}, fmt.Errorf("inspect direct MCP: %w", err)
	}
	if result.ExitCode != 0 {
		message := strings.ToLower(string(append(bytes.Clone(result.Stdout), result.Stderr...)))
		missingMCP := strings.Contains(message, "no mcp server") && strings.Contains(message, "found")
		if strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "no server") || missingMCP {
			return Component{}, nil
		}
		return Component{}, commandError(Command{Name: hostBinary(target.Host), Args: args, Dir: target.Workspace}, result)
	}
	if target.Host == HostCodex {
		component, parseErr := findJSONComponent(result.Stdout, []string{"jetkvm"}, "command")
		if parseErr != nil {
			return Component{}, fmt.Errorf("decode Codex MCP definition: %w", parseErr)
		}
		return component, nil
	}
	return parseClaudeMCP(result.Stdout)
}

func findJSONComponent(data []byte, names []string, sourceKeys ...string) (Component, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return Component{}, err
	}
	object := findNamedObject(value, names)
	if object == nil {
		return Component{}, nil
	}
	component := Component{Present: true}
	for _, key := range sourceKeys {
		if stringValue, ok := object[key].(string); ok && stringValue != "" {
			if key == "command" {
				component.Command = stringValue
			} else {
				component.Source = stringValue
			}
			break
		}
	}
	if version, ok := object["version"].(string); ok {
		component.Version = version
	}
	if args, ok := object["args"].([]any); ok {
		for _, arg := range args {
			stringArg, stringOK := arg.(string)
			if !stringOK {
				return Component{}, errors.New("MCP args must be strings")
			}
			component.Args = append(component.Args, stringArg)
		}
	}
	return component, nil
}

func parseMarketplace(data []byte) (Component, error) {
	object, err := decodeNamedObject(data, []string{MarketplaceName})
	if err != nil || object == nil {
		return Component{}, err
	}
	return Component{Present: true, Source: firstString(
		object["repo"], nestedString(object, "marketplaceSource", "source"),
		object["repository"], object["url"], object["source"],
	)}, nil
}

func parsePlugin(data []byte) (Component, error) {
	object, err := decodeNamedObject(data, []string{MarketplaceName, PluginReference})
	if err != nil || object == nil {
		return Component{}, err
	}
	component := Component{Present: true, Source: firstString(
		object["marketplaceName"], nestedString(object, "marketplaceSource", "source"),
		object["marketplace"], nestedString(object, "source", "source"), object["source"],
	)}
	component.Version, _ = object["version"].(string)
	return component, nil
}

func decodeNamedObject(data []byte, names []string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return findNamedObject(value, names), nil
}

func nestedString(object map[string]any, outer, inner string) any {
	nested, ok := object[outer].(map[string]any)
	if !ok {
		return nil
	}
	return nested[inner]
}

func firstString(values ...any) string {
	for _, value := range values {
		if stringValue, ok := value.(string); ok && stringValue != "" {
			return stringValue
		}
	}
	return ""
}

func findNamedObject(value any, names []string) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"name", "id", "pluginId", "reference", "plugin"} {
			name, ok := typed[key].(string)
			if ok && contains(names, name) {
				return typed
			}
		}
		for _, child := range typed {
			if found := findNamedObject(child, names); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findNamedObject(child, names); found != nil {
				return found
			}
		}
	}
	return nil
}

func parseClaudeMCP(data []byte) (Component, error) {
	text := string(data)
	if !strings.Contains(strings.ToLower(text), "jetkvm") {
		return Component{}, errors.New("Claude MCP response did not identify jetkvm")
	}
	component := Component{Present: true}
	for line := range strings.Lines(text) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "command":
			component.Command = strings.TrimSpace(value)
		case "args", "arguments":
			component.Args = strings.Fields(strings.TrimSpace(value))
		}
	}
	if component.Command == "" {
		return Component{}, errors.New("Claude MCP response omitted command")
	}
	return component, nil
}

func classify(snapshot Snapshot, version string) State {
	marketOwned := !snapshot.Marketplace.Present || sameSource(snapshot.Marketplace.Source, MarketplaceSource)
	pluginOwned := !snapshot.Plugin.Present || sameSource(snapshot.Plugin.Source, MarketplaceName) ||
		sameSource(snapshot.Plugin.Source, MarketplaceSource)
	directEquivalent := equivalentMCP(snapshot.DirectMCP)

	if !marketOwned || !pluginOwned || (snapshot.DirectMCP.Present && !directEquivalent) {
		return StateForeignConflict
	}
	if snapshot.Target.Mode == ModePlugin {
		if directEquivalent && !snapshot.Marketplace.Present && !snapshot.Plugin.Present {
			return StateLegacyDirect
		}
		if snapshot.Marketplace.Present && snapshot.Plugin.Present && !snapshot.DirectMCP.Present {
			if snapshot.Plugin.Version == "" {
				return StatePartial
			}
			if snapshot.Plugin.Version != version {
				return StateOwnedOutdated
			}
			return StateEquivalent
		}
		if !snapshot.Marketplace.Present && !snapshot.Plugin.Present && !snapshot.DirectMCP.Present {
			return StateAbsent
		}
		return StatePartial
	}

	if snapshot.Marketplace.Present || snapshot.Plugin.Present {
		return StateForeignConflict
	}
	if directEquivalent {
		return StateEquivalent
	}
	if !snapshot.DirectMCP.Present {
		return StateAbsent
	}
	return StateForeignConflict
}

func equivalentMCP(component Component) bool {
	return component.Present && component.Command == CanonicalMCPCommand && slicesEqual(component.Args, canonicalMCPArgs())
}

func fingerprint(snapshot Snapshot) (string, error) {
	copy := snapshot
	copy.Fingerprint = ""
	encoded, err := json.Marshal(copy, json.Deterministic(true))
	if err != nil {
		return "", fmt.Errorf("encode setup snapshot: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sameSource(actual, expected string) bool {
	actual = strings.TrimSuffix(strings.TrimSpace(actual), ".git")
	actual = strings.TrimPrefix(actual, "https://github.com/")
	actual = strings.TrimPrefix(actual, "git@github.com:")
	return actual == expected
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hostBinary(host Host) string {
	if host == HostClaudeCode {
		return "claude"
	}
	return "codex"
}
