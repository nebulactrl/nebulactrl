package composeimport

import "testing"

// LoadSourceForTest lets the external test package read testdata fixtures.
func LoadSourceForTest(t *testing.T, dir string) ComposeSource { return loadFixture(t, dir) }
