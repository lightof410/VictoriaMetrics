package main

import (
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmalert/config"
)

func TestParseClusterMemberNum(t *testing.T) {
	f := func(s string, want int) {
		t.Helper()
		n, err := parseClusterMemberNum(s)
		if err != nil {
			t.Fatalf("unexpected error for %q: %s", s, err)
		}
		if n != want {
			t.Fatalf("unexpected result for %q: want %d, got %d", s, want, n)
		}
	}
	f("0", 0)
	f("3", 3)
	f("vmagent-mify-ak-0", 0)
	f("vmagent-mify-ak-1", 1)
	f("vmalert-42", 42)

	if _, err := parseClusterMemberNum("foo"); err == nil {
		t.Fatalf("expected error for non-numeric memberNum")
	}
}

func TestFilterRulesByClusterMember(t *testing.T) {
	mkRule := func(id uint64) config.Rule {
		return config.Rule{ID: id, Alert: "a", Expr: "1"}
	}
	rules := []config.Rule{mkRule(0), mkRule(1), mkRule(2), mkRule(3), mkRule(4)}

	// membersCount <= 1 returns all rules unchanged
	got := filterRulesByClusterMember(rules, 1, 0)
	if len(got) != len(rules) {
		t.Fatalf("unexpected len for membersCount=1: want %d, got %d", len(rules), len(got))
	}

	// distribute rules across 2 members
	got0 := filterRulesByClusterMember(rules, 2, 0)
	got1 := filterRulesByClusterMember(rules, 2, 1)
	if len(got0)+len(got1) != len(rules) {
		t.Fatalf("rules must be fully distributed: got %d+%d != %d", len(got0), len(got1), len(rules))
	}
	for _, r := range got0 {
		if r.ID%2 != 0 {
			t.Fatalf("member 0 unexpectedly got rule with id %d", r.ID)
		}
	}
	for _, r := range got1 {
		if r.ID%2 != 1 {
			t.Fatalf("member 1 unexpectedly got rule with id %d", r.ID)
		}
	}
}
