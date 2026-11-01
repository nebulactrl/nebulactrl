package lynis

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseReportRealDebian13(t *testing.T) {
	f, err := os.Open("testdata/report-debian13-before.dat")
	require.NoError(t, err)
	defer f.Close()
	rep, err := ParseReport(f)
	require.NoError(t, err)

	require.Equal(t, "3.1.7", rep.LynisVersion)
	require.Equal(t, 62, rep.HardeningIndex)
	require.Equal(t, 238, rep.TestsDone)
	require.Equal(t, "Debian 13", rep.OS)
	require.Equal(t, "2026-10-27 09:46:39", rep.FinishedAt)
	require.Equal(t, 1, rep.VulnerablePackages)

	require.Len(t, rep.Warnings, 1)
	require.Equal(t, "PKGS-7392", rep.Warnings[0].ID)
	require.Equal(t, "Found one or more vulnerable packages.", rep.Warnings[0].Text)
	require.Empty(t, rep.Warnings[0].Details, "'-' means no details")

	byID := map[string][]int{}
	for i, s := range rep.Suggestions {
		byID[s.ID] = append(byID[s.ID], i)
	}
	// KRNL-6000 carries one detail per sysctl key; they hang off its finding.
	k := rep.Suggestions[byID["KRNL-6000"][0]]
	require.NotEmpty(t, k.Fields)
	var found bool
	for _, d := range k.Fields {
		if d.Field == "kernel.kptr_restrict" {
			found = true
			require.Equal(t, "2", d.Preferred)
			require.Equal(t, "0", d.Value)
			require.Equal(t, "Restrict access to kernel symbols", d.Description)
		}
	}
	require.True(t, found)
	// SSH-7408 repeats once per option; the details attach to the first finding only.
	ssh := byID["SSH-7408"]
	require.Greater(t, len(ssh), 1)
	require.NotEmpty(t, rep.Suggestions[ssh[0]].Fields)
	require.Empty(t, rep.Suggestions[ssh[1]].Fields)
	// "text:" prefixes of solutions are dropped.
	require.Equal(t, "Use chmod to change file permissions", rep.Suggestions[byID["FILE-7524"][0]].Solution)
}

func TestParseReportRejectsUnfinishedAudit(t *testing.T) {
	_, err := ParseReport(strings.NewReader("lynis_version=3.1.7\nwarning[]=PKGS-7392|x|-|-|\n"))
	require.ErrorContains(t, err, "hardening_index")
	_, err = ParseReport(strings.NewReader("hardening_index=250\n"))
	require.ErrorContains(t, err, "0 to 100")
}

func TestParseReportIgnoresMalformedAndOversized(t *testing.T) {
	var b strings.Builder
	b.WriteString("hardening_index=71\n")
	b.WriteString("suggestion[]=not-an-id|text|-|-|\n")
	b.WriteString("suggestion[]=AUTH-9328\n")
	b.WriteString("details[]=KRNL-6000|sysctl|desc:x;|\n")
	b.WriteString("suggestion[]=AUTH-9328|" + strings.Repeat("x", 5000) + "|-|-|\n")
	for range 700 {
		b.WriteString("suggestion[]=PKGS-7370|many|-|-|\n")
	}
	rep, err := ParseReport(strings.NewReader(b.String()))
	require.NoError(t, err)
	require.Equal(t, 71, rep.HardeningIndex)
	require.Len(t, rep.Suggestions, maxFindings)
	require.Len(t, rep.Suggestions[0].Text, maxTextLength)
}

func TestValidTestID(t *testing.T) {
	for id, ok := range map[string]bool{
		"KRNL-6000": true, "AUTH-9328:03": true, "krnl-6000": false, "KRNL6000": false, "KRNL-600": false, "": false, "KRNL-60a0": false,
	} {
		require.Equal(t, ok, validTestID(id), id)
	}
}
