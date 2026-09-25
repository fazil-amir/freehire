package main

import (
	"os"
	"strconv"
)

// catalogueTechOnly reports the catalogue scope every freehire binary
// boardly-api runs will see: CATALOGUE_TECH_ONLY, inherited from this
// process's environment. It mirrors classify.parseCatalogueTechOnly in the
// root module — which boardly-api may not import — rule for rule: unset or
// unreadable means IT only, because that is what those binaries will do
// with the same value. Shown in the top bar so the mode is never a guess.
func catalogueTechOnly() bool {
	return parseTechOnly(os.Getenv("CATALOGUE_TECH_ONLY"))
}

func parseTechOnly(v string) bool {
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return true
	}
	return b
}
