package agentruntime

func ValidThreadStatus(s ThreadStatus) bool {
	switch s.Type {
	case "notLoaded", "idle", "systemError":
		return len(s.ActiveFlags) == 0
	case "active":
		if len(s.ActiveFlags) > 2 {
			return false
		}
		seen := map[string]bool{}
		for _, flag := range s.ActiveFlags {
			if seen[flag] || flag != "waitingOnApproval" && flag != "waitingOnUserInput" {
				return false
			}
			seen[flag] = true
		}
		return true
	}
	return false
}
