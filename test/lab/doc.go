// Package lab drives the three-machine acceptance lab.
//
// The lab key is a PuTTY .ppk: OpenSSH cannot read it, and puttygen on the development host cannot
// convert it, so plink and pscp are the transport. Every target is pinned by a host-key fingerprint
// from the environment (§4.3 of AGENTS.md): a substituted machine fails the run instead of receiving
// our commands, and the suite never trusts a key it did not already know.
//
// The suite is a manual gate and is compiled only with `-tags=lab`. A developer without lab access
// must still be able to run the default `go test`, so a missing environment skips the tests instead
// of failing them; the skip message names every variable that is missing.
//
// This file carries the package documentation for the default build; the code lives in lab.go and
// the tests behind the tag.
package lab
