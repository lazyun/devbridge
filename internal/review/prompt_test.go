package review

import (
	"strings"
	"testing"
)

func TestBuildPromptReturnsAuthoredGoalByteExact(t *testing.T) {
	goal := "  Inspect the production path.\n\nPreserve spacing.  "
	got, err := BuildPrompt(PromptInput{Instruction: goal})
	if err != nil {
		t.Fatal(err)
	}
	if got != goal {
		t.Fatalf("prompt changed authored bytes: got %q want %q", got, goal)
	}
}

func TestBuildPromptRejectsEmptyOrInvalidGoal(t *testing.T) {
	for _, goal := range []string{"", " \n\t", "contains\x00nul"} {
		if _, err := BuildPrompt(PromptInput{Instruction: goal}); err == nil {
			t.Fatalf("BuildPrompt(%q) succeeded", goal)
		}
	}
}

func TestBuildPromptAcceptsExternalContractMaximumByteExact(t *testing.T) {
	goal := strings.Repeat("x", maxInstructionBytes)
	got, err := BuildPrompt(PromptInput{Instruction: goal})
	if err != nil {
		t.Fatal(err)
	}
	if got != goal {
		t.Fatal("maximum-size authored Review instruction changed")
	}
	if _, err := BuildPrompt(PromptInput{Instruction: goal + "x"}); err == nil {
		t.Fatal("oversized Review instruction succeeded")
	}
}
