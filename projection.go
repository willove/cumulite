package cumulite

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// projection is a compiled top-level projection. Only the flat form of the
// wire contract is honoured — include or exclude whole fields — because dotted
// paths and array-element projection need a projection planner the lite
// engine has none of; those are refused (ErrUnsupported) rather than
// approximated by their prefix, which would hand back a field the caller did
// not ask for.
type projection struct {
	include   bool
	fields    []string
	includeID bool
}

// parseProjection compiles the request value. nil and an empty object mean
// "no projection"; a map follows the document-database convention —
// {"field": 1} includes, {"field": 0} excludes, the two not mixed except for
// _id; a []string is an inclusion list.
func parseProjection(value any) (*projection, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []string:
		if len(v) == 0 {
			return nil, nil
		}
		return includeProjection(v, true)
	case map[string]any:
		if len(v) == 0 {
			return nil, nil
		}
		return parseProjectionObject(v)
	default:
		return nil, fmt.Errorf("cumulite: projection expects an object or field list, got %T", value)
	}
}

func parseProjectionObject(object map[string]any) (*projection, error) {
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		include     []string
		exclude     []string
		idIncluded  = true
		idSpecified bool
	)
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("cumulite: projection with an empty field name")
		}
		if strings.Contains(name, ".") {
			return nil, fmt.Errorf("%w: Projection %q: dotted paths", ErrUnsupported, name)
		}
		flag, ok := projectionFlag(object[name])
		if !ok {
			return nil, fmt.Errorf("cumulite: projection value for %q must be 0 or 1", name)
		}
		if name == "_id" {
			idSpecified = true
			idIncluded = flag
			continue
		}
		if flag {
			include = append(include, name)
		} else {
			exclude = append(exclude, name)
		}
	}
	if len(include) > 0 && len(exclude) > 0 {
		return nil, fmt.Errorf("cumulite: cannot mix inclusion and exclusion in one projection (except for _id)")
	}
	switch {
	case len(include) > 0:
		return includeProjection(include, idIncluded)
	case len(exclude) > 0:
		// _id survives an exclusion projection even when named with 0 — the
		// full engine's server behaves the same way, and a consumer that
		// swaps engines must not see its _id disappear.
		return &projection{include: false, fields: exclude}, nil
	case idSpecified && !idIncluded:
		return &projection{include: false, fields: []string{"_id"}}, nil
	default:
		// Only "_id": 1 was given, which is an inclusion of _id alone.
		return &projection{include: true, fields: nil, includeID: true}, nil
	}
}

func includeProjection(fields []string, includeID bool) (*projection, error) {
	for _, name := range fields {
		if strings.Contains(name, ".") {
			return nil, fmt.Errorf("%w: Projection %q: dotted paths", ErrUnsupported, name)
		}
	}
	return &projection{include: true, fields: fields, includeID: includeID}, nil
}

// projectionFlag reads a 0/1 flag from the map value, accepting the shapes a
// Go caller and a JSON round-trip each produce: bool, int, float64 and
// json.Number.
func projectionFlag(value any) (flag bool, ok bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case int:
		return v == 1, v == 0 || v == 1
	case float64:
		return v == 1, v == 0 || v == 1
	case json.Number:
		return v == "1", v == "0" || v == "1"
	default:
		return false, false
	}
}

// apply returns the projected view of doc. A nil projection passes the
// document through unchanged. The result never shares the top-level map with
// the input, so projecting the same document twice cannot observe holes the
// first pass dug.
func (p *projection) apply(doc map[string]any) map[string]any {
	if p == nil {
		return doc
	}
	if !p.include {
		out := make(map[string]any, len(doc))
		for key, value := range doc {
			out[key] = value
		}
		for _, field := range p.fields {
			delete(out, field)
		}
		return out
	}
	out := make(map[string]any, len(p.fields)+1)
	if p.includeID {
		if id, ok := doc["_id"]; ok {
			out["_id"] = id
		}
	}
	for _, field := range p.fields {
		if value, ok := doc[field]; ok {
			out[field] = value
		}
	}
	return out
}
