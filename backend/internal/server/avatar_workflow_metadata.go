package server

import (
	"encoding/json"
	"gorm.io/datatypes"
)

func preserveAvatarWorkflowMetadata(old, next datatypes.JSON) datatypes.JSON {
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(old, &before)
	_ = json.Unmarshal(next, &after)
	if after == nil {
		after = map[string]json.RawMessage{}
	}
	delete(after, "avatar_v2")
	if value, ok := before["avatar_v2"]; ok {
		after["avatar_v2"] = value
	}
	raw, _ := json.Marshal(after)
	return datatypes.JSON(raw)
}
