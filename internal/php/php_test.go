package php

import "testing"

func TestSatisfies(t *testing.T) {
	cases := []struct {
		version, constraint string
		want                bool
	}{
		{"8.4", "^8.2", true},
		{"8.1", "^8.2", false},
		{"8.3", "^8.1|^8.2", true},
		{"8.3", "^8.1 || ^8.2", true},
		{"8.3", ">=8.1 <8.3", false},
		{"8.2", ">=8.1,<8.3", true},
		{"8.2", "~8.2.0", true},
		{"8.3", "~8.2.0", false},
		{"9.0", "^8.2", false},
		{"8.4", "", true},
		// Composer tilde: ~8.0 is >=8.0 <9.0, ~8.0.1 is >=8.0.1 <8.1.
		{"8.4", "~5.6 || ~7.0 || ~8.0", true},
		{"8.5", "~8", true},
		{"9.0", "~8.0", false},
		{"8.1", "~8.0.1", false},
	}
	for _, c := range cases {
		if got := Satisfies(c.version, c.constraint); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.version, c.constraint, got, c.want)
		}
	}
}

func TestPick(t *testing.T) {
	rts := []Runtime{{Version: "8.4"}, {Version: "8.3"}, {Version: "8.1"}}
	if r, _ := Pick(rts, "", Requirement{Constraints: []string{"^8.2"}}); r.Version != "8.4" {
		t.Errorf("newest satisfying: got %s", r.Version)
	}
	if r, _ := Pick(rts, "", Requirement{Constraints: []string{"~8.1.0"}}); r.Version != "8.1" {
		t.Errorf("tilde: got %s", r.Version)
	}
	if r, _ := Pick(rts, "8.3", Requirement{Constraints: []string{"^8.4"}}); r.Version != "8.3" {
		t.Errorf("explicit choice wins over constraint: got %s", r.Version)
	}
	if _, err := Pick(rts, "7.4", Requirement{Constraints: []string{""}}); err == nil {
		t.Error("missing explicit version should fail")
	}
	if _, err := Pick(rts, "", Requirement{Constraints: []string{"^9.0"}}); err == nil {
		t.Error("unsatisfiable constraint should fail")
	}
}

func TestRequirement(t *testing.T) {
	rts := []Runtime{{Version: "8.5"}, {Version: "8.4"}, {Version: "8.1"}}
	// Laravel 8 accepts "^7.4 || ^8.0" but only supports up to PHP 8.1.
	if r, _ := Pick(rts, "", Requirement{Constraints: []string{"^7.4 || ^8.0"}, Max: "8.1"}); r.Version != "8.1" {
		t.Errorf("max: got %s", r.Version)
	}
	// A locked package capping PHP wins over a permissive root constraint.
	if r, _ := Pick(rts, "", Requirement{Constraints: []string{">=8.1", ">=8.0 <8.5"}}); r.Version != "8.4" {
		t.Errorf("lock cap: got %s", r.Version)
	}
	if _, err := Pick(rts, "", Requirement{Constraints: []string{">=7.1"}, Max: "7.4"}); err == nil {
		t.Error("Laravel 5.5 (max 7.4) must not get PHP 8")
	}
}
