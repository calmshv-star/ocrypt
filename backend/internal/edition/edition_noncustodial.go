//go:build noncustodial

// Package edition reports which build edition a binary belongs to. The
// noncustodial (SaaS) edition is selected with the `noncustodial` build tag and
// links no code that can hold, sign, move or route funds.
package edition

// NonCustodial is true only in the noncustodial edition.
const NonCustodial = true

// Name is the edition label recorded in logs and images.
const Name = "noncustodial"
