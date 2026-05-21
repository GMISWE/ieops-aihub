package scenario

// Scenario defines the interface every scenario plugin must implement.
type Scenario interface {
	Name() string
	PrepareWorkspace(wi WorkItemCtx) error
	OnWrap(wi WorkItemCtx) error
	CleanupWorkspace(wi WorkItemCtx, reason string) error
}

// WorkItemCtx carries context passed to scenario hooks.
type WorkItemCtx struct {
	WorkItemID string
	AttemptID  string
	Project    string
	Goal       string
	WIType     string
	TaskBranch string
}
