package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseChoice(t *testing.T) {
	cases := map[string]string{
		"":        "1", // Enter takes the default
		"\n":      "1",
		"1\n":     "1",
		"3":       "3",
		"6\n":     "6",
		" 5 \n":   "5",
		"0\n":     "0",
		"q\n":     "0",
		"bogus\n": "0",
	}
	for in, want := range cases {
		if got := parseChoice(in); got != want {
			t.Errorf("parseChoice(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReadChoiceEOFIsNotABlankLine: the caller tells "no operator at all" from
// "the operator pressed Ctrl+Z" by whether anything arrived, so an empty read
// has to be distinguishable from a blank line.
func TestReadChoiceEOFIsNotABlankLine(t *testing.T) {
	if line, ok := readChoice(bufio.NewReader(strings.NewReader(""))); ok {
		t.Errorf("readChoice on an empty stream = (%q, true), want ok=false", line)
	}
	if line, ok := readChoice(bufio.NewReader(strings.NewReader("\n"))); !ok || line != "\n" {
		t.Errorf("readChoice(blank line) = (%q, %v), want (\\n, true)", line, ok)
	}
	// A line with no trailing newline still carries the operator's input.
	if line, ok := readChoice(bufio.NewReader(strings.NewReader("3"))); !ok || line != "3" {
		t.Errorf("readChoice(\"3\" at EOF) = (%q, %v), want (3, true)", line, ok)
	}
}
