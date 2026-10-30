package git

type FileStatus struct {
	Path      string
	Original  string
	Index     byte
	Worktree  byte
	Untracked bool
}

type Status struct {
	Files []FileStatus
}

func (s Status) Dirty() bool { return len(s.Files) != 0 }
