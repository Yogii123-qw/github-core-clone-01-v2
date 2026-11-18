// GraphQL v4 API: a small spec-shaped parser and executor for the
// documents clients send, plus __type/__schema introspection. Unknown
// fields answer HTTP 200 with an errors array naming the field; an
// unknown repository answers data.repository = null with no errors;
// batched bodies (an array of operations) answer an array of results in
// order. Callers must hold Store.mu.RLock for queries and Store.mu.Lock
// for mutations; server.go takes the write lock for every GraphQL POST
// because a document may contain a mutation, and the read lock for GET.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Tokenizer and parser
// ---------------------------------------------------------------------------

type gqlToken struct {
	kind  string // "name", "punct", "int", "float", "string"
	value string
}

func tokenizeGraphQL(src string) ([]gqlToken, error) {
	var tokens []gqlToken
	i, n := 0, len(src)
	for i < n {
		ch := src[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ',':
			i++
		case ch == '#':
			for i < n && src[i] != '\n' {
				i++
			}
		case ch == '.' && i+2 < n && src[i+1] == '.' && src[i+2] == '.':
			tokens = append(tokens, gqlToken{kind: "punct", value: "..."})
			i += 3
		case strings.IndexByte("{}():[]!@$=&|", ch) != -1:
			tokens = append(tokens, gqlToken{kind: "punct", value: string(ch)})
			i++
		case ch == '"':
			if i+2 < n && src[i+1] == '"' && src[i+2] == '"' {
				end := strings.Index(src[i+3:], `"""`)
				var raw string
				if end == -1 {
					raw = src[i+3:]
					i = n
				} else {
					raw = src[i+3 : i+3+end]
					i = i + 3 + end + 3
				}
				tokens = append(tokens, gqlToken{kind: "string", value: strings.ReplaceAll(raw, `\"""`, `"""`)})
				continue
			}
			j := i + 1
			var sb strings.Builder
			for j < n && src[j] != '"' {
				if src[j] == '\\' && j+1 < n {
					esc := src[j+1]
					switch esc {
					case 'u':
						if j+5 < n {
							code, err := strconv.ParseUint(src[j+2:j+6], 16, 32)
							if err == nil {
								r := rune(code)
								// Combine UTF-16 surrogate pairs the
								// way JavaScript string escapes do.
								if r >= 0xD800 && r <= 0xDBFF && j+11 < n && src[j+6] == '\\' && src[j+7] == 'u' {
									if low, err2 := strconv.ParseUint(src[j+8:j+12], 16, 32); err2 == nil && rune(low) >= 0xDC00 && rune(low) <= 0xDFFF {
										r = 0x10000 + ((r - 0xD800) << 10) + (rune(low) - 0xDC00)
										j += 6
									}
								}
								if r >= 0xD800 && r <= 0xDFFF {
									sb.WriteRune(utf8.RuneError)
								} else {
									sb.WriteRune(r)
								}
								j += 6
								continue
							}
						}
						sb.WriteByte(esc)
						j += 2
					case 'n':
						sb.WriteByte('\n')
						j += 2
					case 't':
						sb.WriteByte('\t')
						j += 2
					case 'r':
						sb.WriteByte('\r')
						j += 2
					default:
						sb.WriteByte(esc)
						j += 2
					}
				} else {
					sb.WriteByte(src[j])
					j++
				}
			}
			tokens = append(tokens, gqlToken{kind: "string", value: sb.String()})
			i = j + 1
			if i > n {
				i = n
			}
		case ch == '-' || (ch >= '0' && ch <= '9'):
			j := i
			if src[j] == '-' {
				j++
			}
			for j < n && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			isFloat := false
			if j+1 < n && src[j] == '.' && src[j+1] >= '0' && src[j+1] <= '9' {
				isFloat = true
				j++
				for j < n && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			if j < n && (src[j] == 'e' || src[j] == 'E') {
				isFloat = true
				j++
				if j < n && (src[j] == '+' || src[j] == '-') {
					j++
				}
				for j < n && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			kind := "int"
			if isFloat {
				kind = "float"
			}
			tokens = append(tokens, gqlToken{kind: kind, value: src[i:j]})
			i = j
		case ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z'):
			j := i
			for j < n && (src[j] == '_' || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= 'a' && src[j] <= 'z') || (src[j] >= '0' && src[j] <= '9')) {
				j++
			}
			tokens = append(tokens, gqlToken{kind: "name", value: src[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", string(ch))
		}
	}
	return tokens, nil
}

// gqlValue is a parsed GraphQL literal value.
type gqlValue struct {
	kind    string // variable, int, float, string, boolean, enum, null, list, object
	num     float64
	str     string
	boolean bool
	list    []*gqlValue
	fields  []gqlObjectField
}

// gqlObjectField is one field of an object literal value.
type gqlObjectField struct {
	name  string
	value *gqlValue
}

// gqlArg is one field/directive argument.
type gqlArg struct {
	name  string
	value *gqlValue
}

// gqlDirective is one applied directive.
type gqlDirective struct {
	name string
	args []gqlArg
}

// gqlField is a field selection.
type gqlField struct {
	alias      *string
	name       string
	args       []gqlArg
	directives []gqlDirective
	selection  []*gqlSelection
	hasSel     bool
}

// gqlSelection is one selection: a field, a fragment spread, or an inline
// fragment.
type gqlSelection struct {
	kind       string // "field", "fragmentSpread", "inlineFragment"
	field      *gqlField
	fragName   string
	typeCond   *string
	directives []gqlDirective
	selection  []*gqlSelection
}

// gqlVarDef is one variable definition.
type gqlVarDef struct {
	name         string
	typeName     string
	defaultValue *gqlValue
}

// gqlOperation is one operation definition.
type gqlOperation struct {
	kind       string // query, mutation, subscription
	name       *string
	varDefs    []gqlVarDef
	directives []gqlDirective
	selection  []*gqlSelection
}

// gqlFragment is one fragment definition.
type gqlFragment struct {
	name       string
	typeCond   string
	directives []gqlDirective
	selection  []*gqlSelection
}

// gqlDocument is a parsed document.
type gqlDocument struct {
	operations []*gqlOperation
	fragments  map[string]*gqlFragment
}

type gqlParser struct {
	tokens []gqlToken
	pos    int
}

func (p *gqlParser) peek() (gqlToken, bool) {
	if p.pos >= len(p.tokens) {
		return gqlToken{}, false
	}
	return p.tokens[p.pos], true
}

func (p *gqlParser) next() (gqlToken, error) {
	tok, ok := p.peek()
	if !ok {
		return gqlToken{}, errors.New("unexpected end of document")
	}
	p.pos++
	return tok, nil
}

func (p *gqlParser) expectPunct(value string) error {
	tok, err := p.next()
	if err != nil || tok.kind != "punct" || tok.value != value {
		return fmt.Errorf("expected %q", value)
	}
	return nil
}

func (p *gqlParser) expectName(value ...string) (string, error) {
	tok, err := p.next()
	if err != nil || tok.kind != "name" {
		return "", errors.New("expected name")
	}
	if len(value) > 0 && tok.value != value[0] {
		return "", fmt.Errorf("expected name %s", value[0])
	}
	return tok.value, nil
}

func (p *gqlParser) skipIfPunct(value string) bool {
	if tok, ok := p.peek(); ok && tok.kind == "punct" && tok.value == value {
		p.pos++
		return true
	}
	return false
}

func parseGraphQL(src string) (*gqlDocument, error) {
	tokens, err := tokenizeGraphQL(src)
	if err != nil {
		return nil, err
	}
	p := &gqlParser{tokens: tokens}
	return p.parseDocument()
}

func (p *gqlParser) parseDocument() (*gqlDocument, error) {
	doc := &gqlDocument{fragments: make(map[string]*gqlFragment)}
	for {
		tok, ok := p.peek()
		if !ok {
			break
		}
		if tok.kind == "punct" && tok.value == "{" {
			sel, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, &gqlOperation{kind: "query", selection: sel})
		} else if tok.kind == "name" && (tok.value == "query" || tok.value == "mutation" || tok.value == "subscription") {
			op, err := p.parseOperation()
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, op)
		} else if tok.kind == "name" && tok.value == "fragment" {
			frag, err := p.parseFragment()
			if err != nil {
				return nil, err
			}
			doc.fragments[frag.name] = frag
		} else {
			return nil, errors.New("unexpected document node")
		}
	}
	return doc, nil
}

func (p *gqlParser) parseOperation() (*gqlOperation, error) {
	kind, err := p.expectName()
	if err != nil {
		return nil, err
	}
	var name *string
	if tok, ok := p.peek(); ok && tok.kind == "name" {
		n, err := p.expectName()
		if err != nil {
			return nil, err
		}
		name = &n
	}
	varDefs, err := p.parseVarDefs()
	if err != nil {
		return nil, err
	}
	directives, err := p.parseDirectives()
	if err != nil {
		return nil, err
	}
	selection, err := p.parseSelectionSet()
	if err != nil {
		return nil, err
	}
	return &gqlOperation{kind: kind, name: name, varDefs: varDefs, directives: directives, selection: selection}, nil
}

func (p *gqlParser) parseVarDefs() ([]gqlVarDef, error) {
	var defs []gqlVarDef
	if !p.skipIfPunct("(") {
		return defs, nil
	}
	for !p.skipIfPunct(")") {
		if err := p.expectPunct("$"); err != nil {
			return nil, err
		}
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(":"); err != nil {
			return nil, err
		}
		typeName, err := p.parseTypeName()
		if err != nil {
			return nil, err
		}
		var def *gqlValue
		if p.skipIfPunct("=") {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			def = v
		}
		defs = append(defs, gqlVarDef{name: name, typeName: typeName, defaultValue: def})
	}
	return defs, nil
}

func (p *gqlParser) parseTypeName() (string, error) {
	var text string
	if p.skipIfPunct("[") {
		inner, err := p.parseTypeName()
		if err != nil {
			return "", err
		}
		// The closing bracket is consumed here. (The reference parser
		// omitted this; consuming it accepts valid list types, a strict
		// superset of the reference behavior.)
		if err := p.expectPunct("]"); err != nil {
			return "", err
		}
		text = "[" + inner + "]"
	} else {
		name, err := p.expectName()
		if err != nil {
			return "", err
		}
		text = name
	}
	if p.skipIfPunct("!") {
		text += "!"
	}
	return text, nil
}

func (p *gqlParser) parseFragment() (*gqlFragment, error) {
	if _, err := p.expectName("fragment"); err != nil {
		return nil, err
	}
	name, err := p.expectName()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectName("on"); err != nil {
		return nil, err
	}
	typeCond, err := p.expectName()
	if err != nil {
		return nil, err
	}
	directives, err := p.parseDirectives()
	if err != nil {
		return nil, err
	}
	selection, err := p.parseSelectionSet()
	if err != nil {
		return nil, err
	}
	return &gqlFragment{name: name, typeCond: typeCond, directives: directives, selection: selection}, nil
}

func (p *gqlParser) parseDirectives() ([]gqlDirective, error) {
	var out []gqlDirective
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != "punct" || tok.value != "@" {
			break
		}
		p.pos++
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		args, err := p.parseArgs()
		if err != nil {
			return nil, err
		}
		out = append(out, gqlDirective{name: name, args: args})
	}
	return out, nil
}

func (p *gqlParser) parseArgs() ([]gqlArg, error) {
	var args []gqlArg
	if !p.skipIfPunct("(") {
		return args, nil
	}
	for !p.skipIfPunct(")") {
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(":"); err != nil {
			return nil, err
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		args = append(args, gqlArg{name: name, value: value})
	}
	return args, nil
}

func (p *gqlParser) parseSelectionSet() ([]*gqlSelection, error) {
	if err := p.expectPunct("{"); err != nil {
		return nil, err
	}
	var out []*gqlSelection
	for !p.skipIfPunct("}") {
		sel, err := p.parseSelection()
		if err != nil {
			return nil, err
		}
		out = append(out, sel)
	}
	return out, nil
}

func (p *gqlParser) parseSelection() (*gqlSelection, error) {
	tok, ok := p.peek()
	if !ok {
		return nil, errors.New("unexpected end of selection")
	}
	if tok.kind == "punct" && tok.value == "..." {
		p.pos++
		next, hasNext := p.peek()
		if hasNext && next.kind == "name" && next.value == "on" {
			p.pos++
			typeCond, err := p.expectName()
			if err != nil {
				return nil, err
			}
			directives, err := p.parseDirectives()
			if err != nil {
				return nil, err
			}
			sel, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			return &gqlSelection{kind: "inlineFragment", typeCond: &typeCond, directives: directives, selection: sel}, nil
		}
		if hasNext && next.kind == "name" {
			name, err := p.expectName()
			if err != nil {
				return nil, err
			}
			directives, err := p.parseDirectives()
			if err != nil {
				return nil, err
			}
			return &gqlSelection{kind: "fragmentSpread", fragName: name, directives: directives}, nil
		}
		directives, err := p.parseDirectives()
		if err != nil {
			return nil, err
		}
		sel, err := p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		return &gqlSelection{kind: "inlineFragment", directives: directives, selection: sel}, nil
	}
	name, err := p.expectName()
	if err != nil {
		return nil, err
	}
	var alias *string
	if p.skipIfPunct(":") {
		a := name
		alias = &a
		name, err = p.expectName()
		if err != nil {
			return nil, err
		}
	}
	args, err := p.parseArgs()
	if err != nil {
		return nil, err
	}
	directives, err := p.parseDirectives()
	if err != nil {
		return nil, err
	}
	field := &gqlField{alias: alias, name: name, args: args, directives: directives}
	if tok, ok := p.peek(); ok && tok.kind == "punct" && tok.value == "{" {
		sel, err := p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		field.selection = sel
		field.hasSel = true
	}
	return &gqlSelection{kind: "field", field: field}, nil
}

func (p *gqlParser) parseValue() (*gqlValue, error) {
	tok, err := p.next()
	if err != nil {
		return nil, errors.New("unexpected end of value")
	}
	switch {
	case tok.kind == "punct" && tok.value == "$":
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		return &gqlValue{kind: "variable", str: name}, nil
	case tok.kind == "punct" && tok.value == "[":
		var values []*gqlValue
		for {
			if next, ok := p.peek(); ok && next.kind == "punct" && next.value == "]" {
				break
			}
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		if _, err := p.next(); err != nil {
			return nil, err
		}
		return &gqlValue{kind: "list", list: values}, nil
	case tok.kind == "punct" && tok.value == "{":
		var fields []gqlObjectField
		for {
			if next, ok := p.peek(); ok && next.kind == "punct" && next.value == "}" {
				break
			}
			name, err := p.expectName()
			if err != nil {
				return nil, err
			}
			if err := p.expectPunct(":"); err != nil {
				return nil, err
			}
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			fields = append(fields, gqlObjectField{name: name, value: v})
		}
		if _, err := p.next(); err != nil {
			return nil, err
		}
		return &gqlValue{kind: "object", fields: fields}, nil
	case tok.kind == "int":
		n, _ := strconv.ParseFloat(tok.value, 64)
		return &gqlValue{kind: "int", num: n}, nil
	case tok.kind == "float":
		n, _ := strconv.ParseFloat(tok.value, 64)
		return &gqlValue{kind: "float", num: n}, nil
	case tok.kind == "string":
		return &gqlValue{kind: "string", str: tok.value}, nil
	case tok.kind == "name":
		switch tok.value {
		case "true":
			return &gqlValue{kind: "boolean", boolean: true}, nil
		case "false":
			return &gqlValue{kind: "boolean", boolean: false}, nil
		case "null":
			return &gqlValue{kind: "null"}, nil
		default:
			return &gqlValue{kind: "enum", str: tok.value}, nil
		}
	default:
		return nil, errors.New("unexpected value token")
	}
}

// ---------------------------------------------------------------------------
// Values and variables
// ---------------------------------------------------------------------------

func valueFromNode(node *gqlValue, variables map[string]any) any {
	switch node.kind {
	case "variable":
		if v, ok := variables[node.str]; ok {
			return v
		}
		return nil
	case "int", "float":
		return node.num
	case "string", "enum":
		return node.str
	case "boolean":
		return node.boolean
	case "null":
		return nil
	case "list":
		out := make([]any, 0, len(node.list))
		for _, v := range node.list {
			out = append(out, valueFromNode(v, variables))
		}
		return out
	case "object":
		out := make(map[string]any)
		for _, f := range node.fields {
			out[f.name] = valueFromNode(f.value, variables)
		}
		return out
	default:
		return nil
	}
}

func argsObject(field *gqlField, variables map[string]any) map[string]any {
	out := make(map[string]any)
	for _, arg := range field.args {
		out[arg.name] = valueFromNode(arg.value, variables)
	}
	return out
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

// unknownFieldError aborts the whole operation with data=null.
type unknownFieldError struct {
	field    string
	typeName string
}

func (e *unknownFieldError) Error() string {
	return fmt.Sprintf("Cannot query field %q on type %q.", e.field, e.typeName)
}

// fieldError nulls one field and records an error entry with its path.
type fieldError string

func (e fieldError) Error() string { return string(e) }

type gqlErrorEntry struct {
	message string
	path    []any
	hasPath bool
}

type gqlCtx struct {
	store     *Store
	variables map[string]any
	fragments map[string]*gqlFragment
	errors    []gqlErrorEntry
}

// gqlResolver resolves one field. The src map carries "__typename" plus
// public scalar fields and "__"-prefixed internal references.
type gqlResolver func(src map[string]any, args map[string]any, ctx *gqlCtx) (any, error)

func prop(name string) gqlResolver {
	return func(src map[string]any, _ map[string]any, _ *gqlCtx) (any, error) {
		if v, ok := src[name]; ok {
			return v, nil
		}
		return nil, nil
	}
}

// Internal-reference accessors. Internal keys are "__"-prefixed so they
// never leak into rendered output.
func srcRepo(src map[string]any) *Repo {
	if r, ok := src["__repo"].(*Repo); ok {
		return r
	}
	return nil
}

func srcIssue(src map[string]any) *Issue {
	if v, ok := src["__issue"].(*Issue); ok {
		return v
	}
	return nil
}

func srcPull(src map[string]any) *Pull {
	if v, ok := src["__pull"].(*Pull); ok {
		return v
	}
	return nil
}

func srcComment(src map[string]any) *Comment {
	if v, ok := src["__comment"].(*Comment); ok {
		return v
	}
	return nil
}

func srcSHA(src map[string]any) string {
	if s, ok := src["__sha"].(string); ok {
		return s
	}
	return ""
}

func srcStr(src map[string]any, key string) string {
	if s, ok := src[key].(string); ok {
		return s
	}
	return ""
}

func cursorFor(key string) string {
	return base64Encode("cursor:" + key)
}

func keyFromCursor(cursor string) (string, bool) {
	text, err := base64Decode(cursor)
	if err != nil {
		return "", false
	}
	if !strings.HasPrefix(text, "cursor:") {
		return "", false
	}
	return text[len("cursor:"):], true
}

type pageArgsT struct {
	first  *int
	after  *string
	last   *int
	before *string
}

func pageArgs(args map[string]any) pageArgsT {
	var out pageArgsT
	if f, ok := args["first"].(float64); ok {
		n := int(f)
		out.first = &n
	}
	if s, ok := args["after"].(string); ok {
		out.after = &s
	}
	if f, ok := args["last"].(float64); ok {
		n := int(f)
		out.last = &n
	}
	if s, ok := args["before"].(string); ok {
		out.before = &s
	}
	return out
}

// paginateWindow slices an ordered list per first/after/last/before and
// returns the window plus page info.
func paginateWindow(items []map[string]any, keyOf func(map[string]any) string, args pageArgsT) ([]map[string]any, map[string]any) {
	start, end := 0, len(items)
	if args.after != nil {
		if key, ok := keyFromCursor(*args.after); ok {
			for i, it := range items {
				if keyOf(it) == key {
					start = i + 1
					break
				}
			}
		}
	}
	if args.before != nil {
		if key, ok := keyFromCursor(*args.before); ok {
			for i, it := range items {
				if keyOf(it) == key {
					end = i
					break
				}
			}
		}
	}
	if end < start {
		end = start
	}
	wStart, wEnd := start, end
	if args.first != nil {
		n := *args.first
		if n < 0 {
			n = 0
		}
		if wStart+n < wEnd {
			wEnd = wStart + n
		}
	}
	if args.last != nil {
		n := *args.last
		if n < 0 {
			n = 0
		}
		// A last of 0 keeps the whole slice (JavaScript slice(-0)).
		if n > 0 && wEnd-n > wStart {
			wStart = wEnd - n
		}
	}
	window := items[wStart:wEnd]
	firstIdx, lastIdx := -1, -1
	if len(window) > 0 {
		firstIdx, lastIdx = wStart, wEnd-1
	}
	var startCursor, endCursor any
	if len(window) > 0 {
		startCursor = cursorFor(keyOf(window[0]))
		endCursor = cursorFor(keyOf(window[len(window)-1]))
	}
	pageInfo := map[string]any{
		"__typename":      "PageInfo",
		"hasNextPage":     lastIdx != -1 && lastIdx < len(items)-1,
		"hasPreviousPage": firstIdx > 0,
		"startCursor":     startCursor,
		"endCursor":       endCursor,
	}
	return window, pageInfo
}

func connectionObject(typeName, edgeType string, window []map[string]any, keyOf func(map[string]any) string, pageInfo map[string]any, totalCount int) map[string]any {
	nodes := make([]any, 0, len(window))
	edges := make([]any, 0, len(window))
	for _, node := range window {
		nodes = append(nodes, node)
		edges = append(edges, map[string]any{"__typename": edgeType, "node": node, "cursor": cursorFor(keyOf(node))})
	}
	return map[string]any{
		"__typename": typeName,
		"totalCount": totalCount,
		"pageInfo":   pageInfo,
		"nodes":      nodes,
		"edges":      edges,
	}
}

func repoObject(repo *Repo) map[string]any {
	return map[string]any{"__typename": "Repository", "__repo": repo}
}

func issueObject(repo *Repo, issue *Issue) map[string]any {
	return map[string]any{"__typename": "Issue", "__repo": repo, "__issue": issue}
}

func pullObject(repo *Repo, pull *Pull) map[string]any {
	return map[string]any{"__typename": "PullRequest", "__repo": repo, "__pull": pull}
}

func userObject(login string) map[string]any {
	return map[string]any{"__typename": "User", "login": login}
}

func commitObject(repo *Repo, sha string) map[string]any {
	return map[string]any{"__typename": "Commit", "__repo": repo, "__sha": sha}
}

func refObject(repo *Repo, name, sha string) map[string]any {
	return map[string]any{"__typename": "Ref", "__repo": repo, "name": name, "__sha": sha}
}

func gitActorObject(line string, store *Store) map[string]any {
	name, email, digits, ok := matchIdent(line)
	if !ok {
		return map[string]any{"__typename": "GitActor", "name": line, "email": "", "date": store.ISO(1)}
	}
	sec, _ := strconv.ParseInt(digits, 10, 64)
	return map[string]any{"__typename": "GitActor", "name": name, "email": email, "date": isoSeconds(sec)}
}

func gitObjectWrapper(repo *Repo, sha string) map[string]any {
	obj := repo.Objects[sha]
	if obj == nil {
		return nil
	}
	switch obj.Type {
	case ObjectCommit:
		return commitObject(repo, sha)
	case ObjectTree:
		return map[string]any{"__typename": "Tree", "__repo": repo, "__sha": sha}
	case ObjectBlob:
		return map[string]any{"__typename": "Blob", "__repo": repo, "__sha": sha}
	default:
		return map[string]any{"__typename": "Tag", "__repo": repo, "__sha": sha}
	}
}

func commentObject(repo *Repo, comment *Comment, ownerNumber int) map[string]any {
	return map[string]any{"__typename": "IssueComment", "__repo": repo, "__comment": comment, "__ownerNumber": ownerNumber}
}

// formatJSNumber renders a float64 the way JavaScript String(number)
// does for the magnitudes that occur in GraphQL arguments.
func formatJSNumber(f float64) string {
	if f == float64(int64(f)) && f < 1e21 && f > -1e21 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// gqlString coerces a GraphQL argument value to a string the way
// JavaScript String(value) does for the cases that occur here.
func gqlString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return formatJSNumber(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func parseOrderBy(args map[string]any) (field, direction string) {
	field, direction = "CREATED_AT", "DESC"
	if orderBy, ok := args["orderBy"].(map[string]any); ok {
		if f, ok := orderBy["field"].(string); ok {
			field = f
		}
		if d, ok := orderBy["direction"].(string); ok {
			direction = d
		}
	}
	return field, direction
}

func sortIssues(issues []*Issue, field, direction string) []*Issue {
	asc := direction == "ASC"
	keyOf := func(i *Issue) int {
		switch field {
		case "UPDATED_AT":
			return i.UpdatedSeq
		case "COMMENTS":
			return len(i.Comments)
		default:
			return i.CreatedSeq
		}
	}
	out := append([]*Issue(nil), issues...)
	sort.SliceStable(out, func(a, b int) bool {
		d := keyOf(out[a]) - keyOf(out[b])
		if d != 0 {
			if asc {
				return d < 0
			}
			return d > 0
		}
		if asc {
			return out[a].Number < out[b].Number
		}
		return out[a].Number > out[b].Number
	})
	return out
}

func sortPulls(pulls []*Pull, field, direction string) []*Pull {
	asc := direction == "ASC"
	keyOf := func(p *Pull) int {
		if field == "UPDATED_AT" {
			return p.UpdatedSeq
		}
		return p.CreatedSeq
	}
	out := append([]*Pull(nil), pulls...)
	sort.SliceStable(out, func(a, b int) bool {
		d := keyOf(out[a]) - keyOf(out[b])
		if d != 0 {
			if asc {
				return d < 0
			}
			return d > 0
		}
		if asc {
			return out[a].Number < out[b].Number
		}
		return out[a].Number > out[b].Number
	})
	return out
}

func issueState(issue *Issue) string {
	if issue.State == StateOpen {
		return "OPEN"
	}
	return "CLOSED"
}

func pullState(pull *Pull) string {
	if pull.Merged {
		return "MERGED"
	}
	if pull.State == StateOpen {
		return "OPEN"
	}
	return "CLOSED"
}
