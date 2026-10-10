package proto

const OpListApps = "list_apps"

// ListAppsArgs filters launchable desktop applications by name or executable path.
// Limit is 50 when omitted, and at most 200. Total counts matches before limiting.
type ListAppsArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// AppLaunch can be passed directly to vm_launch, with the VM added by the caller.
// Shortcut arguments and working directory are preserved.
type AppLaunch struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Cwd  string   `json:"cwd"`
}

type AppWindow struct {
	Handle uint64 `json:"handle"`
	PID    uint32 `json:"pid"`
	Title  string `json:"title"`
}

type AppInfo struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Launch  AppLaunch   `json:"launch"`
	Running bool        `json:"running"`
	Windows []AppWindow `json:"windows"`
}

type ListAppsResult struct {
	Apps      []AppInfo `json:"apps"`
	Total     int       `json:"total"`
	Truncated bool      `json:"truncated"`
	Warnings  []string  `json:"warnings"`
}
