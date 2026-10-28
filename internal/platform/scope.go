package platform

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// identRe constrains resource names so typos fail loudly at parse
// time instead of matching nothing forever.
var identRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Grant is one permission in resource:action[:qualifier] form — the
// grammar shared by API token scopes (stage 9 phase 2) and plugin
// declared permissions (phase 3).
//
//	resource  what part of the notebook ("note", "search", ...)
//	action    "read" or "write"
//	qualifier optional glob narrowing the resource, e.g. "projects/*"
//
// "*" may stand in for resource or action (a grant of exactly "*" is
// shorthand for "*:*"). An empty qualifier matches everything; a
// qualifier on the grant requires the request to carry one.
type Grant struct {
	Resource  string
	Action    string
	Qualifier string
}

// ParseGrants parses a grant list separated by spaces or commas.
// Malformed entries fail the whole list: a typo in a permission must
// not silently widen it.
func ParseGrants(spec string) ([]Grant, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ','
	})
	gs := make([]Grant, 0, len(fields))
	for _, f := range fields {
		g, err := parseGrant(f)
		if err != nil {
			return nil, err
		}
		gs = append(gs, g)
	}
	return gs, nil
}

func parseGrant(s string) (Grant, error) {
	if s == "*" {
		return Grant{Resource: "*", Action: "*"}, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return Grant{}, fmt.Errorf("invalid grant %q: want resource:action[:qualifier]", s)
	}
	g := Grant{Resource: parts[0], Action: parts[1]}
	if len(parts) == 3 {
		g.Qualifier = parts[2]
	}
	if g.Resource == "" || g.Action == "" {
		return Grant{}, fmt.Errorf("invalid grant %q: empty resource or action", s)
	}
	if g.Resource != "*" && !identRe.MatchString(g.Resource) {
		return Grant{}, fmt.Errorf("invalid grant %q: bad resource", s)
	}
	switch g.Action {
	case "*", "read", "write":
	default:
		return Grant{}, fmt.Errorf("invalid grant %q: action must be read, write, or *", s)
	}
	if g.Qualifier != "" && g.Qualifier != "*" {
		if _, err := path.Match(g.Qualifier, ""); err != nil {
			return Grant{}, fmt.Errorf("invalid grant %q: bad qualifier pattern", s)
		}
	}
	return g, nil
}

// GrantsAllow reports whether any grant covers the request. A request
// with an empty qualifier is covered only by grants that do not
// narrow by qualifier.
func GrantsAllow(gs []Grant, resource, action, qualifier string) bool {
	for _, g := range gs {
		if g.Matches(resource, action, qualifier) {
			return true
		}
	}
	return false
}

// Matches implements one grant against a concrete request.
func (g Grant) Matches(resource, action, qualifier string) bool {
	if g.Resource != "*" && g.Resource != resource {
		return false
	}
	if g.Action != "*" && g.Action != action {
		return false
	}
	switch g.Qualifier {
	case "", "*":
		return true
	}
	if qualifier == "" {
		return false
	}
	if ok, err := path.Match(g.Qualifier, qualifier); err == nil && ok {
		return true
	}
	// Trailing "/*" also covers the whole subtree below it, which
	// path.Match's single-segment semantics would otherwise miss:
	// "projects/*" grants "projects/a/b".
	if strings.HasSuffix(g.Qualifier, "/*") {
		return strings.HasPrefix(qualifier, g.Qualifier[:len(g.Qualifier)-1])
	}
	return false
}

// Spec re-renders grants in canonical space-separated form.
func GrantsSpec(gs []Grant) string {
	parts := make([]string, 0, len(gs))
	for _, g := range gs {
		if g.Resource == "*" && g.Action == "*" && g.Qualifier == "" {
			parts = append(parts, "*")
			continue
		}
		s := g.Resource + ":" + g.Action
		if g.Qualifier != "" {
			s += ":" + g.Qualifier
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
