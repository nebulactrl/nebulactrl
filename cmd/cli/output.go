package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

// printJSON writes v as indented JSON to stdout.
func (c *cli) printJSON(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := c.out.Write(buf.Bytes())
	return err
}

// table writes aligned columns to stdout.
func (c *cli) table(headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// info prints a human-oriented line unless --json is set.
func (c *cli) info(format string, args ...any) {
	if !c.asJSON {
		fmt.Fprintf(c.out, format+"\n", args...)
	}
}

// note prints progress and hints to stderr unless --json is set.
func (c *cli) note(format string, args ...any) {
	if !c.asJSON {
		fmt.Fprintf(c.errOut, format+"\n", args...)
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func ts(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func tsPtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return ts(*t)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// itemsOrEmpty makes a nil list marshal as [] in --json output.
func itemsOrEmpty[T any](p *[]T) []T {
	if p == nil {
		return []T{}
	}
	return *p
}
