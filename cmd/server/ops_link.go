package main

import (
	"fmt"
	"strconv"
	"strings"
)

// OpsLink is a typed cross-reference among tickets, changes, incidents, hosts, SLO, SQL.
type OpsLink struct {
	Type string `json:"type"`           // incident|ticket|change|host|slo|sql_change|datasource|k8s|service|alert
	ID   string `json:"id"`             // string form of id
	Role string `json:"role,omitempty"` // caused_by|affects|implements|related|audit
	Name string `json:"name,omitempty"` // optional display hint
}

func normalizeOpsLink(l OpsLink) (OpsLink, bool) {
	l.Type = strings.ToLower(strings.TrimSpace(l.Type))
	l.ID = strings.TrimSpace(l.ID)
	l.Role = strings.ToLower(strings.TrimSpace(l.Role))
	l.Name = strings.TrimSpace(l.Name)
	switch l.Type {
	case "incident", "ticket", "change", "host", "slo", "sql_change", "datasource", "k8s", "service", "alert":
	default:
		return OpsLink{}, false
	}
	if l.ID == "" {
		return OpsLink{}, false
	}
	if l.Role == "" {
		l.Role = "related"
	}
	return l, true
}

func mergeOpsLinks(existing []OpsLink, add ...OpsLink) []OpsLink {
	out := append([]OpsLink{}, existing...)
	seen := map[string]bool{}
	for _, l := range out {
		seen[l.Type+":"+l.ID+":"+l.Role] = true
	}
	for _, raw := range add {
		l, ok := normalizeOpsLink(raw)
		if !ok {
			continue
		}
		key := l.Type + ":" + l.ID + ":" + l.Role
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	return out
}

func removeOpsLink(existing []OpsLink, typ, id, role string) []OpsLink {
	typ = strings.ToLower(strings.TrimSpace(typ))
	id = strings.TrimSpace(id)
	role = strings.ToLower(strings.TrimSpace(role))
	var out []OpsLink
	for _, l := range existing {
		if l.Type == typ && l.ID == id && (role == "" || l.Role == role) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func opsLinkIDInt(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func parseOpsLinkInt(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	return n
}

func hostOpsLink(hostID, hostname string) OpsLink {
	return OpsLink{Type: "host", ID: hostID, Role: "affects", Name: firstNonEmptyOrDash(hostname, hostID)}
}

func incidentOpsLink(id int64, role string) OpsLink {
	if role == "" {
		role = "related"
	}
	return OpsLink{Type: "incident", ID: opsLinkIDInt(id), Role: role}
}

func sloOpsLink(id string) OpsLink {
	return OpsLink{Type: "slo", ID: id, Role: "caused_by"}
}

func sqlChangeOpsLink(id string) OpsLink {
	return OpsLink{Type: "sql_change", ID: id, Role: "implements"}
}

func changeOpsLink(id int64) OpsLink {
	return OpsLink{Type: "change", ID: opsLinkIDInt(id), Role: "related"}
}

func ticketOpsLink(id int64) OpsLink {
	return OpsLink{Type: "ticket", ID: opsLinkIDInt(id), Role: "related"}
}

func formatOpsLinksHint(links []OpsLink) string {
	if len(links) == 0 {
		return ""
	}
	parts := make([]string, 0, len(links))
	for i, l := range links {
		if i >= 8 {
			parts = append(parts, fmt.Sprintf("…+%d", len(links)-8))
			break
		}
		label := l.Type + ":" + l.ID
		if l.Name != "" {
			label += "(" + l.Name + ")"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}
