package inventory

func Static() Catalog {
	definitions := []ToolDefinition{
		observeTool("jetkvm_list_devices", false, "list_devices_input", "device_list_output"),
		observeTool("jetkvm_get_status", true, "device_input", "device_status_output"),
		observeTool("jetkvm_get_capabilities", true, "capabilities_input", "capabilities_output"),
		observeTool("jetkvm_get_operation", true, "operation_input", "operation_receipt_output"),
		videoControlTool("jetkvm_open_control", true, "open_control_input", "control_output"),
		videoReadTool("jetkvm_get_control", "control_input", "control_output"),
		videoControlTool("jetkvm_close_control", false, "control_input", "operation_receipt_output"),
		videoReadTool("jetkvm_observe", "observe_input", "observation_output"),
		videoReadTool("jetkvm_capture_screen", "capture_input", "observation_output"),
		videoReadTool("jetkvm_wait_for_signal", "wait_signal_input", "observation_output"),
		writeTool("jetkvm_key_press", "input", EffectInput, false, "key_press_input", "operation_receipt_output", "hid"),
		writeTool("jetkvm_key_combo", "input", EffectInput, false, "key_combo_input", "operation_receipt_output", "hid"),
		writeTool("jetkvm_type_text", "input", EffectInput, false, "type_text_input", "operation_receipt_output", "hid"),
		writeTool("jetkvm_pointer_click", "input", EffectInput, false, "pointer_click_input", "operation_receipt_output", "pointer"),
		writeTool("jetkvm_release_input", "input", EffectInput, false, "release_input_input", "operation_receipt_output", "hid"),
		writeTool("jetkvm_run_actions", "input", EffectInput, false, "run_actions_input", "action_batch_output", "hid"),
		observeToolFor("jetkvm_get_power_state", "power", true, "device_input", "power_state_output", "atx"),
		writeTool("jetkvm_power_action", "power", EffectPower, true, "power_action_input", "operation_receipt_output", "atx"),
		writeTool("jetkvm_wake", "power", EffectPower, false, "wake_input", "operation_receipt_output", "wol"),
	}
	catalog, err := New(definitions)
	if err != nil {
		panic(err)
	}
	return catalog
}

func observeTool(name string, scoped bool, input, output string) ToolDefinition {
	return observeToolFor(name, "observe", scoped, input, output, "")
}

func observeToolFor(name, toolset string, scoped bool, input, output, capability string) ToolDefinition {
	return ToolDefinition{
		Name: name, Toolset: toolset, Effect: EffectObserve,
		ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false,
		DeviceScoped: scoped, Capability: capability, InputSchema: input, OutputSchema: output,
		Retry: RetryBoundedRead, Receipt: ReceiptObservation,
	}
}

func videoReadTool(name, input, output string) ToolDefinition {
	return ToolDefinition{
		Name: name, Toolset: "video", Effect: EffectObserve,
		ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false,
		DeviceScoped: true, Capability: "video", InputSchema: input, OutputSchema: output,
		Retry: RetryBoundedRead, Receipt: ReceiptObservation,
	}
}

func videoControlTool(name string, destructive bool, input, output string) ToolDefinition {
	return ToolDefinition{
		Name: name, Toolset: "video", Effect: EffectObserve,
		ReadOnly: false, Destructive: destructive, Idempotent: false, OpenWorld: false,
		DeviceScoped: true, Capability: "video", InputSchema: input, OutputSchema: output,
		Retry: RetryNeverAfterSend, Receipt: ReceiptOperation,
	}
}

func writeTool(name, toolset string, effect EffectClass, destructive bool, input, output, capability string) ToolDefinition {
	return ToolDefinition{
		Name: name, Toolset: toolset, Effect: effect,
		ReadOnly: false, Destructive: destructive, Idempotent: false, OpenWorld: false,
		DeviceScoped: true, Capability: capability, InputSchema: input, OutputSchema: output,
		Retry: RetryNeverAfterSend, Receipt: ReceiptOperation,
	}
}
