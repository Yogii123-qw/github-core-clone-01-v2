package main

import (
	"strings"
	"testing"
)

func argByName(field *gqlField, name string) *gqlValue {
	for _, a := range field.args {
		if a.name == name {
			return a.value
		}
	}
	return nil
}

func gqlResultJSON(t *testing.T, store *Store, body string) (int, string) {
	t.Helper()
	parsed, err := unmarshalJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	result, err := HandleGraphQL(store, parsed)
	if err != nil {
		t.Fatal(err)
	}
	out, err := marshalJSON(result.JSON)
	if err != nil {
		t.Fatal(err)
	}
	return result.Status, string(out)
}

func TestGraphQLParseBasic(t *testing.T) {
	doc, err := parseGraphQL(`{ viewer { login id } }`)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(doc.operations))
	}
	op := doc.operations[0]
	if op.kind != "query" {
		t.Fatalf("anonymous operation kind = %q, want query", op.kind)
	}
	if len(op.selection) != 1 || op.selection[0].field == nil || op.selection[0].field.name != "viewer" {
		t.Fatalf("selection = %+v", op.selection)
	}
	viewer := op.selection[0].field
	if len(viewer.selection) != 2 || viewer.selection[0].field.name != "login" || viewer.selection[1].field.name != "id" {
		t.Fatalf("viewer sub-selections = %+v", viewer.selection)
	}
}

func TestGraphQLParseVariablesFragmentsDirectives(t *testing.T) {
	src := `
		query GetRepo($owner: String!, $name: String = "r", $ids: [ID!]) {
			repository(owner: $owner, name: $name) {
				...RepoBits
				name @include(if: true)
				description @skip(if: false)
			}
		}
		fragment RepoBits on Repository { id nameWithOwner }
	`
	doc, err := parseGraphQL(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.operations) != 1 || doc.operations[0].name == nil || *doc.operations[0].name != "GetRepo" {
		t.Fatalf("operation = %+v", doc.operations)
	}
	op := doc.operations[0]
	if len(op.varDefs) != 3 {
		t.Fatalf("varDefs = %d, want 3", len(op.varDefs))
	}
	if op.varDefs[0].typeName != "String!" {
		t.Fatalf("var type = %q", op.varDefs[0].typeName)
	}
	if op.varDefs[1].defaultValue == nil || op.varDefs[1].defaultValue.str != "r" {
		t.Fatalf("default value = %+v", op.varDefs[1].defaultValue)
	}
	if op.varDefs[2].typeName != "[ID!]" {
		t.Fatalf("list var type = %q, want [ID!]", op.varDefs[2].typeName)
	}
	frag, ok := doc.fragments["RepoBits"]
	if !ok || frag.typeCond != "Repository" {
		t.Fatalf("fragment = %+v ok=%v", frag, ok)
	}
	repoField := op.selection[0].field
	if len(repoField.selection) != 3 {
		t.Fatalf("repository selections = %d, want 3", len(repoField.selection))
	}
	if repoField.selection[0].kind != "fragmentSpread" || repoField.selection[0].fragName != "RepoBits" {
		t.Fatalf("first selection = %+v", repoField.selection[0])
	}
	if len(repoField.selection[1].field.directives) != 1 || repoField.selection[1].field.directives[0].name != "include" {
		t.Fatalf("directives = %+v", repoField.selection[1].field.directives)
	}
}

func TestGraphQLParseValues(t *testing.T) {
	doc, err := parseGraphQL(`{ f(a: 42, b: 1.5, c: true, d: null, e: ENUM, f: [1, "x"], g: {h: 2}, i: $var) }`)
	if err != nil {
		t.Fatal(err)
	}
	field := doc.operations[0].selection[0].field
	a := argByName(field, "a")
	if a == nil || a.num != 42 || a.kind != "int" {
		t.Fatalf("int arg = %+v", a)
	}
	b := argByName(field, "b")
	if b == nil || b.num != 1.5 || b.kind != "float" {
		t.Fatalf("float arg = %+v", b)
	}
	c := argByName(field, "c")
	if c == nil || !c.boolean || c.kind != "boolean" {
		t.Fatalf("boolean arg = %+v", c)
	}
	d := argByName(field, "d")
	if d == nil || d.kind != "null" {
		t.Fatalf("null arg = %+v", d)
	}
	e := argByName(field, "e")
	if e == nil || e.str != "ENUM" || e.kind != "enum" {
		t.Fatalf("enum arg = %+v", e)
	}
	f := argByName(field, "f")
	if f == nil || len(f.list) != 2 {
		t.Fatalf("list arg = %+v", f)
	}
	g := argByName(field, "g")
	if g == nil || len(g.fields) != 1 || g.fields[0].name != "h" {
		t.Fatalf("object arg = %+v", g)
	}
	i := argByName(field, "i")
	if i == nil || i.kind != "variable" || i.str != "var" {
		t.Fatalf("variable arg = %+v", i)
	}
}

func TestGraphQLParseStrings(t *testing.T) {
	// Escapes, surrogate pairs, and block strings.
	doc, err := parseGraphQL(`{ f(a: "Aé😀", b: """line1
line2""", c: "tab\there") }`)
	if err != nil {
		t.Fatal(err)
	}
	field := doc.operations[0].selection[0].field
	if got := argByName(field, "a"); got == nil || got.str != "Aé😀" {
		t.Fatalf("escaped string = %+v", got)
	}
	if got := argByName(field, "b"); got == nil || got.str != "line1\nline2" {
		t.Fatalf("block string = %+v", got)
	}
	if got := argByName(field, "c"); got == nil || got.str != "tab\there" {
		t.Fatalf("escape = %+v", got)
	}
	// Explicit surrogate-pair escape.
	doc2, err := parseGraphQL(`{ f(a: "\uD83D\uDE00") }`)
	if err != nil {
		t.Fatal(err)
	}
	if got := argByName(doc2.operations[0].selection[0].field, "a"); got == nil || got.str != "😀" {
		t.Fatalf("surrogate pair = %+v", got)
	}
}

func TestGraphQLParseErrors(t *testing.T) {
	// An empty source parses to an empty document; the envelope layer
	// rejects empty queries before parsing (see TestGraphQLErrors).
	doc, err := parseGraphQL(``)
	if err != nil || len(doc.operations) != 0 {
		t.Fatalf("empty source must parse to an empty document, got %+v err=%v", doc, err)
	}
	bad := []string{
		`{`,
		`{ viewer `,
		`{ viewer { login }`,
		`query { f(a: ) }`,
		`{ f(a: "unterminated) }`,
		`fragment on Repository { id }`,
		`{ ... }`,
	}
	for _, src := range bad {
		if _, err := parseGraphQL(src); err == nil {
			t.Fatalf("parseGraphQL(%q) must error", src)
		}
	}
}

func TestGraphQLValueFromNode(t *testing.T) {
	vars := map[string]any{"n": float64(7), "s": "hi"}
	cases := []struct {
		node *gqlValue
		want any
	}{
		{&gqlValue{kind: "int", num: 3}, float64(3)},
		{&gqlValue{kind: "string", str: "x"}, "x"},
		{&gqlValue{kind: "boolean", boolean: true}, true},
		{&gqlValue{kind: "null"}, nil},
		{&gqlValue{kind: "enum", str: "OPEN"}, "OPEN"},
		{&gqlValue{kind: "variable", str: "n"}, float64(7)},
		{&gqlValue{kind: "variable", str: "s"}, "hi"},
		{&gqlValue{kind: "variable", str: "missing"}, nil},
	}
	for _, tc := range cases {
		got := valueFromNode(tc.node, vars)
		if got != tc.want {
			t.Fatalf("valueFromNode(%+v) = %#v, want %#v", tc.node, got, tc.want)
		}
	}
	list := valueFromNode(&gqlValue{kind: "list", list: []*gqlValue{{kind: "int", num: 1}, {kind: "variable", str: "n"}}}, vars)
	items, ok := list.([]any)
	if !ok || len(items) != 2 || items[1] != float64(7) {
		t.Fatalf("list value = %#v", list)
	}
}

func TestGraphQLExecuteViewer(t *testing.T) {
	status, json := gqlResultJSON(t, NewStore(), `{"query": "{ viewer { login } }"}`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(json, `"adaiversen"`) {
		t.Fatalf("viewer result = %s", json)
	}
}

func TestGraphQLErrors(t *testing.T) {
	store := NewStore()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"non-object", `42`, "request body must be an object"},
		{"missing-query", `{}`, "must provide a query string"},
		{"syntax", `{"query": "{ "}`, "syntax error"},
		{"no-operation", `{"query": "fragment F on Repository { id }"}`, "no operation found"},
	}
	for _, tc := range cases {
		_, json := gqlResultJSON(t, store, tc.body)
		if !strings.Contains(json, tc.want) {
			t.Fatalf("%s: result = %s, want substring %q", tc.name, json, tc.want)
		}
	}
	// Unknown field: data null + errors array, HTTP 200.
	status, json := gqlResultJSON(t, store, `{"query": "{ nosuchfield } "}`)
	if status != 200 || !strings.Contains(json, `"data":null`) || !strings.Contains(json, "errors") {
		t.Fatalf("unknown field result = %d %s", status, json)
	}
}

func TestGraphQLBatch(t *testing.T) {
	status, json := gqlResultJSON(t, NewStore(), `[{"query": "{ viewer { login } }"}, {"query": "{ rateLimit { limit } }"}]`)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(strings.TrimSpace(json), "[") {
		t.Fatalf("batch result must be an array: %s", json)
	}
	if !strings.Contains(json, "adaiversen") || !strings.Contains(json, "5000") {
		t.Fatalf("batch result = %s", json)
	}
}
