package main

import (
	"context"
	"testing"
)

func TestDestroyArgsExplicitRuntimeAndDataPolicy(t *testing.T) {
	for _, volumes := range []bool{false, true} {
		args := []string{"--runtime", "0123456789abcdef"}
		if volumes {
			args = append(args, "--volumes")
		}
		id, got, err := destroyArgs(context.Background(), args)
		if err != nil || id != "0123456789abcdef" || got != volumes {
			t.Fatalf("got %q %v %v", id, got, err)
		}
	}
}

func TestDestroyArgsRejectsAmbiguousOrInvalidTargets(t *testing.T) {
	for _, args := range [][]string{
		{"--runtime", ""},
		{"--runtime", "../../foreign"},
		{"--runtime", "0123456789abcdef", "--worktree", "."},
		{"--all"},
		{"0123456789abcdef"},
		{"--runtime", "0123456789abcdef", "unexpected"},
	} {
		if _, _, err := destroyArgs(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
