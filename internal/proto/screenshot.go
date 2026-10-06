package proto

// ScreenshotResult describes the pixels captured by the agent before host-side cropping or scaling.
type ScreenshotResult struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	OriginX   int    `json:"origin_x"`
	OriginY   int    `json:"origin_y"`
	SessionID uint32 `json:"session_id"`
	Console   bool   `json:"console"`
}
