package plugin

import "setpoint/internal/task"

func ResultContract(metadata Metadata) task.ResultContract {
	itemIDs := make([]string, 0, len(metadata.Checks))
	for _, definition := range metadata.Checks {
		itemIDs = append(itemIDs, definition.ID)
	}
	return task.ResultContract{
		PluginID: metadata.ID, PluginVersion: metadata.Version, ItemIDs: itemIDs,
	}
}
