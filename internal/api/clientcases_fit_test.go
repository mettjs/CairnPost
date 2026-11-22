package api

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// resolved follows a reference to what it names.
func resolved(doc node, v any) any {
	s, _ := v.(node)
	ref, ok := s["$ref"].(string)
	if !ok {
		return v
	}
	var at any = doc
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		next, _ := at.(node)
		at = next[part]
	}
	return resolved(doc, at)
}

// misfit says where a JSON value is not what a schema describes: a field
// it must have and lacks, one the schema does not name, a value of
// another kind or outside a closed list.
func misfit(doc node, schema, v any, where string) []string {
	s, _ := resolved(doc, schema).(node)
	if s == nil {
		return []string{where + ": the API describes no JSON here"}
	}
	if forms, ok := s["oneOf"].([]any); ok {
		for _, form := range forms {
			if len(misfit(doc, form, v, where)) == 0 {
				return nil
			}
		}
		return []string{where + ": fits none of the forms the API describes"}
	}
	is := map[string]string{"string": "string", "bool": "boolean", "float64": "number", "<nil>": "null",
		"map[string]interface {}": "object", "[]interface {}": "array"}[fmt.Sprintf("%T", v)]
	if n, ok := v.(float64); ok && n == math.Trunc(n) {
		is = "integer"
	}
	kind, _ := s["type"].(string)
	if is != kind && !(is == "integer" && kind == "number") {
		return []string{fmt.Sprintf("%s: is a %s, where the API describes a %s", where, is, kind)}
	}
	var out []string
	switch v := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(node)
		must, _ := s["required"].([]any)
		for _, name := range must {
			if _, ok := v[name.(string)]; !ok {
				out = append(out, fmt.Sprintf("%s: lacks %q, which the API always says", where, name))
			}
		}
		for name, value := range v {
			of, ok := props[name]
			if !ok {
				of, ok = s["additionalProperties"]
			}
			if !ok || of == false {
				out = append(out, fmt.Sprintf("%s: says %q, which the API does not describe", where, name))
				continue
			}
			out = append(out, misfit(doc, of, value, where+"."+name)...)
		}
	case []any:
		for i, value := range v {
			out = append(out, misfit(doc, s["items"], value, fmt.Sprintf("%s[%d]", where, i))...)
		}
	default:
		if listed, ok := s["enum"].([]any); ok && !slices.Contains(listed, any(v)) {
			return []string{fmt.Sprintf("%s: %v is not one of %v", where, v, listed)}
		}
	}
	return out
}

// answered says where an answer is not one the API describes for a call:
// its status, and then its JSON, its bytes or its having no body.
func answered(doc, op node, a answerCase, where string) []string {
	if (a.JSON == nil) == (a.Text == nil) {
		return []string{where + ": an answer has a json or a text, and not both"}
	}
	answers, _ := op["responses"].(node)
	said, _ := resolved(doc, answers[strconv.Itoa(a.Status)]).(node)
	content, _ := said["content"].(node)
	switch {
	case said == nil:
		return []string{fmt.Sprintf("%s: the API describes no %d for this call", where, a.Status)}
	case a.JSON != nil:
		var v any
		if err := json.Unmarshal(a.JSON, &v); err != nil {
			return []string{where + ": " + err.Error()}
		}
		schema, _ := content["application/json"].(node)
		return misfit(doc, schema["schema"], v, where)
	case len(content) == 0 && (*a.Text != "" || a.Headers != nil):
		return []string{fmt.Sprintf("%s: the API answers %d with no body", where, a.Status)}
	case len(content) != 0 && content[a.Headers["Content-Type"]] == nil:
		return []string{fmt.Sprintf("%s: the API does not answer %d as %q", where, a.Status, a.Headers["Content-Type"])}
	}
	return nil
}

// sent says where what a call sends is not what its route takes: a body
// of another shape, a query value or an Idempotency-Key the route has
// no use for.
func sent(doc, op node, c clientCall) []string {
	var out []string
	takes := map[string]bool{}
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		p, _ := resolved(doc, p).(node)
		takes[fmt.Sprint(p["in"], " ", p["name"])] = true
	}
	for name := range c.Sends.Query {
		if !takes["query "+name] {
			out = append(out, fmt.Sprintf("%s: sends %q in the query, which %s does not take", c.Name, name, c.Route))
		}
	}
	if c.Sends.IdempotencyKey != "" && !takes["header Idempotency-Key"] {
		out = append(out, fmt.Sprintf("%s: sends an Idempotency-Key, which %s does not take", c.Name, c.Route))
	}
	asked, _ := resolved(doc, op["requestBody"]).(node)
	content, _ := asked["content"].(node)
	schema, _ := content["application/json"].(node)
	switch {
	case c.Sends.Body == nil && asked["required"] == true:
		out = append(out, fmt.Sprintf("%s: sends no body, and %s needs one", c.Name, c.Route))
	case c.Sends.Body != nil:
		var v any
		if err := json.Unmarshal(c.Sends.Body, &v); err != nil {
			return append(out, c.Name+": "+err.Error())
		}
		out = append(out, misfit(doc, schema["schema"], v, "what "+c.Name+" sends")...)
	}
	return out
}
