package memory

import (
	"regexp"
	"sort"
)

func mustKeywordRE() *regexp.Regexp {
	return regexp.MustCompile(`[a-z0-9_./-]{4,}`)
}

// sortByScoreDesc orders blocks by rank_score descending, stably (V8's Array.sort is stable, so a
// stable sort preserves the source-list order for equal scores — keeping selection deterministic).
func sortByScoreDesc(blocks []Block) {
	sort.SliceStable(blocks, func(i, j int) bool {
		return blocks[i].RankScore > blocks[j].RankScore
	})
}
