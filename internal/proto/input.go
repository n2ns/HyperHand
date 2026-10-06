package proto

const OpTypeKeys = "type_keys"

type TypeKeysArgs struct {
	Text   string `json:"text"`
	Handle uint64 `json:"handle"`
	PID    uint32 `json:"pid"`
}

type TypeKeysResult struct {
	Events int `json:"events"`
}
