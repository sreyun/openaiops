package main

import (
	"fmt"
	"strings"
)

// ScanBaselineDiff summarizes finding churn vs the previous completed scan.
type ScanBaselineDiff struct {
	PreviousScanID string   `json:"previous_scan_id,omitempty"`
	Added          int      `json:"added"`
	Removed        int      `json:"removed"`
	Worsened       int      `json:"worsened"` // same key, higher severity
	Improved       int      `json:"improved"`
	SamplesAdded   []string `json:"samples_added,omitempty"`
	SamplesRemoved []string `json:"samples_removed,omitempty"`
}

func hostFindingDiffKey(f HostFinding) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(f.Category)),
		strings.ToLower(strings.TrimSpace(f.ID)),
		strings.ToLower(strings.TrimSpace(f.CVE)),
		strings.ToLower(strings.TrimSpace(f.Package)),
		strings.ToLower(strings.TrimSpace(f.Title)),
	}
	return strings.Join(parts, "|")
}

func webFindingDiffKey(f WebFinding) string {
	return strings.ToLower(strings.TrimSpace(f.TemplateID)) + "|" +
		strings.ToLower(strings.TrimSpace(f.MatcherName)) + "|" +
		strings.ToLower(strings.TrimSpace(firstNonEmpty(f.URL, f.MatchedAt)))
}

func hostSeverityRank(level string) int {
	return findingLevelRank(level)
}

func webSeverityRank(sev string) int {
	return findingLevelRank(sev)
}

func diffHostFindings(prev, cur []HostFinding, prevScanID string) *ScanBaselineDiff {
	if len(prev) == 0 && len(cur) == 0 {
		return nil
	}
	out := &ScanBaselineDiff{PreviousScanID: prevScanID}
	prevMap := map[string]HostFinding{}
	for _, f := range prev {
		prevMap[hostFindingDiffKey(f)] = f
	}
	curMap := map[string]HostFinding{}
	for _, f := range cur {
		curMap[hostFindingDiffKey(f)] = f
	}
	for k, f := range curMap {
		if old, ok := prevMap[k]; !ok {
			out.Added++
			if len(out.SamplesAdded) < 5 {
				out.SamplesAdded = append(out.SamplesAdded, truncateRun(f.Title, 80))
			}
		} else if hostSeverityRank(f.Level) > hostSeverityRank(old.Level) {
			out.Worsened++
		} else if hostSeverityRank(f.Level) < hostSeverityRank(old.Level) {
			out.Improved++
		}
	}
	for k, f := range prevMap {
		if _, ok := curMap[k]; !ok {
			out.Removed++
			if len(out.SamplesRemoved) < 5 {
				out.SamplesRemoved = append(out.SamplesRemoved, truncateRun(f.Title, 80))
			}
		}
	}
	return out
}

func diffWebFindings(prev, cur []WebFinding, prevScanID string) *ScanBaselineDiff {
	if len(prev) == 0 && len(cur) == 0 {
		return nil
	}
	out := &ScanBaselineDiff{PreviousScanID: prevScanID}
	prevMap := map[string]WebFinding{}
	for _, f := range prev {
		prevMap[webFindingDiffKey(f)] = f
	}
	curMap := map[string]WebFinding{}
	for _, f := range cur {
		curMap[webFindingDiffKey(f)] = f
	}
	for k, f := range curMap {
		if old, ok := prevMap[k]; !ok {
			out.Added++
			if len(out.SamplesAdded) < 5 {
				out.SamplesAdded = append(out.SamplesAdded, truncateRun(firstNonEmptyOrDash(f.Name, f.TemplateID), 80))
			}
		} else if webSeverityRank(f.Severity) > webSeverityRank(old.Severity) {
			out.Worsened++
		} else if webSeverityRank(f.Severity) < webSeverityRank(old.Severity) {
			out.Improved++
		}
	}
	for k, f := range prevMap {
		if _, ok := curMap[k]; !ok {
			out.Removed++
			if len(out.SamplesRemoved) < 5 {
				out.SamplesRemoved = append(out.SamplesRemoved, truncateRun(firstNonEmptyOrDash(f.Name, f.TemplateID), 80))
			}
		}
	}
	return out
}

func formatBaselineDiffHint(d *ScanBaselineDiff) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("较上次：新增 %d · 消失 %d · 恶化 %d · 缓解 %d", d.Added, d.Removed, d.Worsened, d.Improved)
}
