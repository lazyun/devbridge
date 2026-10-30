package evidence

func Artifacts(claim AgentClaim, git GitEvidence) []Artifact {
	return []Artifact{{Kind: ArtifactAgentMessage, Content: claim.Message}, {Kind: ArtifactGitDiff, Content: git.Diff}}
}
