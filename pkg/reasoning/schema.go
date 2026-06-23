package reasoning

import (
	"context"
	"strings"
)

const provenanceStatusProbeCypher = "CALL TABLE_INFO('RelatesToNode_') RETURN *"

func probeProvenanceStatus(ctx context.Context, g GraphQuerier) bool {
	if g == nil {
		return false
	}
	rows, err := g.Query(ctx, provenanceStatusProbeCypher, nil)
	if err != nil {
		return false
	}
	return tableInfoHasColumn(rows, "status")
}

func tableInfoHasColumn(rows []map[string]any, column string) bool {
	want := normalizeSchemaName(column)
	if want == "" {
		return false
	}
	for _, row := range rows {
		for _, value := range row {
			if normalizeSchemaName(asString(value)) == want {
				return true
			}
		}
	}
	return false
}

func normalizeSchemaName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.Trim(s, "`\"'")
	if dot := strings.LastIndexByte(s, '.'); dot >= 0 {
		s = s[dot+1:]
	}
	return s
}
