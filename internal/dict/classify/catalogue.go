package classify

import (
	"log"
	"os"
	"strconv"
	"sync"
)

// catalogueTechOnlyEnv is the one switch between an IT-only catalogue (the default) and one
// that accepts every posting. It is read process-wide rather than threaded through each
// binary because the gates it opens must agree with each other: ingest storing a posting that
// the search drain then refuses to index, or that prune then hard-deletes, is worse than
// either setting on its own. Every binary touching those gates — ingest, search-drain,
// reindex, recount-companies, prune, server (link import) — must see the same value.
const catalogueTechOnlyEnv = "CATALOGUE_TECH_ONLY"

// CatalogueTechOnly reports whether the catalogue is restricted to technical postings.
// Unset means true. When false:
//   - ingest stores non-technical postings instead of rejecting them (pipeline.outOfCatalogue);
//   - search indexes jobs no dictionary placed (search.CategoryUnresolved);
//   - prune's three IT-scope rules stop deleting (cmd/prune matchRule).
//
// Enrichment, embeddings and the sitemap stay gated on is_tech regardless: those spend
// LLM budget or a search engine's daily quota, and opening them is a separate decision.
//
// An unreadable value keeps the catalogue tech-only and says so, rather than failing:
// cmd/server reads this lazily on a request path, where a fatal exit would take the site
// down over a typo.
var CatalogueTechOnly = sync.OnceValue(func() bool {
	return parseCatalogueTechOnly(os.Getenv(catalogueTechOnlyEnv))
})

func parseCatalogueTechOnly(v string) bool {
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("classify: %s=%q is not a boolean — keeping the catalogue tech-only", catalogueTechOnlyEnv, v)
		return true
	}
	if !b {
		log.Printf("classify: %s=false — non-technical postings are accepted into the catalogue", catalogueTechOnlyEnv)
	}
	return b
}
