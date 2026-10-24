package app

// Reader responses have no unrecorded field, even for an admin using the reader alias.
type forecastReaderGroupsResponse struct {
	*ForecastGroupsMetadata
	OK             bool                  `json:"ok"`
	Mode           string                `json:"mode"`
	Official       bool                  `json:"official"`
	Range          forecastRange         `json:"range"`
	Checkpoints    []string              `json:"checkpoints"`
	Groups         []forecastReaderGroup `json:"groups"`
	ExcludedMonths []forecastPurgedMonth `json:"excluded_months,omitempty"`
	Scope          string                `json:"scope"`
	ScopeOU        string                `json:"scope_ou,omitempty"`
}

type forecastReaderGroup struct {
	Label    string                   `json:"label"`
	Start    string                   `json:"start"`
	End      string                   `json:"end"`
	Official bool                     `json:"official"`
	Bench    string                   `json:"bench"`
	PurgedAt string                   `json:"purged_at,omitempty"`
	Coverage *forecastCoverage        `json:"coverage,omitempty"`
	Cells    map[string]*forecastCell `json:"cells"`
}

type forecastReaderMembersResponse struct {
	OK             bool                  `json:"ok"`
	Label          string                `json:"label"`
	Start          string                `json:"start"`
	End            string                `json:"end"`
	Bench          string                `json:"bench"`
	Checkpoint     string                `json:"checkpoint"`
	State          string                `json:"state"`
	Population     string                `json:"population,omitempty"`
	PurgedAt       string                `json:"purged_at,omitempty"`
	ExcludedMonths []forecastPurgedMonth `json:"excluded_months,omitempty"`
	Resolved       *int                  `json:"resolved,omitempty"`
	Total          *int                  `json:"total,omitempty"`
	Refused        *int                  `json:"refused,omitempty"`
	CorpAction     *int                  `json:"corp_action,omitempty"`
	N              *int                  `json:"n,omitempty"`
	Members        []forecastMember      `json:"members"`
	NextCursor     string                `json:"next_cursor,omitempty"`
}

func forecastReaderMembers(out forecastMembersResponse, d forecastDecision, cursor string) forecastReaderMembersResponse {
	r := forecastReaderMembersResponse{OK: out.OK, Label: out.Label, Start: out.Start, End: out.End,
		Bench: out.Bench, Checkpoint: out.Checkpoint, State: out.State, Members: out.Members, NextCursor: cursor,
		Population: out.Population, PurgedAt: out.PurgedAt, ExcludedMonths: out.ExcludedMonths}
	if !d.External || d.Scope != scopeAll {
		r.N, r.Total, r.Resolved, r.Refused, r.CorpAction = &out.N, &out.Total, &out.Resolved, &out.Refused, &out.CorpAction
	}
	return r
}

func forecastReaderGroups(resp forecastGroupsResponse, d forecastDecision) forecastReaderGroupsResponse {
	out := forecastReaderGroupsResponse{OK: resp.OK, Mode: resp.Mode, Official: resp.Official,
		Range: resp.Range, Checkpoints: resp.Checkpoints, Groups: make([]forecastReaderGroup, 0, len(resp.Groups)), ExcludedMonths: resp.ExcludedMonths,
		Scope: resp.Scope, ScopeOU: resp.ScopeOU}
	hideVolume := d.External && d.Scope == scopeAll
	if !hideVolume {
		out.ForecastGroupsMetadata = resp.ForecastGroupsMetadata
	}
	for _, g := range resp.Groups {
		rg := forecastReaderGroup{Label: g.Label, Start: g.Start, End: g.End, Official: g.Official,
			Bench: g.Bench, Cells: g.Cells, PurgedAt: g.PurgedAt}
		if !hideVolume {
			cov := g.Coverage
			rg.Coverage = &cov
		} else {
			rg.Cells = make(map[string]*forecastCell, len(g.Cells))
			for cp, cell := range g.Cells {
				rg.Cells[cp] = forecastCellWithoutVolume(cell)
			}
		}
		out.Groups = append(out.Groups, rg)
	}
	return out
}

// Copy before shaping: cached cells are shared with admins and own-population readers. Slices
// carry their own denominators, so hiding only the top-level n would still disclose volume.
func forecastCellWithoutVolume(cell *forecastCell) *forecastCell {
	if cell == nil {
		return nil
	}
	if cell.State != "mature" {
		return &forecastCell{State: cell.State}
	}
	out := *cell
	out.MinNMet = true
	out.PendingReasons, out.FailedCapExceeded = nil, nil
	out.NotBefore, out.Expected = "", false
	out.N, out.Resolved, out.Total, out.Refused, out.CorpAction = nil, nil, nil, nil, nil
	out.ICN, out.HitN, out.BandN, out.CalibN = nil, nil, nil, nil
	if cell.Slices != nil {
		out.Slices = make([]forecastSlice, len(cell.Slices))
		for i, slice := range cell.Slices {
			out.Slices[i] = forecastSlice{PolicyVersion: slice.PolicyVersion,
				forecastCell: *forecastCellWithoutVolume(&slice.forecastCell)}
		}
	}
	return &out
}
