// Command assemble prints the self-contained installer the control plane serves
// at /install.sh (install.sh with its assets and libraries inlined), for the
// installer test harness.
package main

import (
	"fmt"

	installdeploy "github.com/nebulactrl/nebulactrl/deploy/install"
)

func main() { fmt.Print(installdeploy.Script()) }
