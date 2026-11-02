//go:build noncustodial

// In the noncustodial edition financial-worker is a stub: the edition links no code that
// can build, sign, broadcast, sweep or refund transfers.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(unavailable(os.Stderr)) }

func unavailable(output io.Writer) int {
	fmt.Fprintln(output, "financial-worker is not part of the noncustodial edition: the operator never signs, moves or refunds funds")
	return 2
}
