package review

import "strings"

const maxInstructionBytes = 64 << 10

type ModuleSummary struct{ Key, Summary string }
type PromptInput struct {
	Instruction     string
	ProjectGuidance string
	Primary         []ModuleSummary
	Supporting      []ModuleSummary
	FocusPaths      []string
}

// BuildPrompt validates and returns the GPT-authored Review instruction
// without changing a byte. Review focus and module selections are durable
// routing metadata, not natural-language prompt material.
func BuildPrompt(in PromptInput) (string, error) {
	if strings.TrimSpace(in.Instruction) == "" || strings.ContainsRune(in.Instruction, '\x00') || len(in.Instruction) > maxInstructionBytes || len(in.ProjectGuidance) > 16384 || len(in.Primary) > 128 || len(in.Supporting) > 128 || len(in.FocusPaths) > 128 {
		return "", ErrInvalidArgument
	}
	for _, items := range [][]ModuleSummary{in.Primary, in.Supporting} {
		for _, m := range items {
			if strings.TrimSpace(m.Key) == "" || len(m.Key) > 128 || len(m.Summary) > 4096 {
				return "", ErrInvalidArgument
			}
		}
	}
	for _, p := range in.FocusPaths {
		if strings.TrimSpace(p) == "" || len(p) > 1024 {
			return "", ErrInvalidArgument
		}
	}
	return in.Instruction, nil
}
