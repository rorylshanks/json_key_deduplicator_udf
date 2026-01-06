package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/valyala/fastjson"
)

type node interface {
	Write(*bytes.Buffer)
	Dedup() node
}

type valueKind int

const (
	kindString valueKind = iota
	kindNumber
	kindBool
	kindNull
)

type valueNode struct {
	kind valueKind
	str  string
	num  string
	b    bool
}

func (v *valueNode) Write(buf *bytes.Buffer) {
	switch v.kind {
	case kindString:
		writeJSONString(buf, v.str)
	case kindNumber:
		buf.WriteString(v.num)
	case kindBool:
		if v.b {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case kindNull:
		buf.WriteString("null")
	}
}

func (v *valueNode) Dedup() node {
	return v
}

type objectEntry struct {
	key   string
	value node
}

type objectNode struct {
	entries []objectEntry
}

func (o *objectNode) Write(buf *bytes.Buffer) {
	buf.WriteByte('{')
	for i, entry := range o.entries {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(buf, entry.key)
		buf.WriteByte(':')
		entry.value.Write(buf)
	}
	buf.WriteByte('}')
}

func (o *objectNode) Dedup() node {
	if len(o.entries) == 0 {
		return o
	}

	o.entries = expandDottedEntries(o.entries)

	for i := range o.entries {
		o.entries[i].value = o.entries[i].value.Dedup()
	}

	firstNonEmpty := make(map[string]int)
	lastIndex := make(map[string]int)

	for i, entry := range o.entries {
		lastIndex[entry.key] = i
		if _, ok := firstNonEmpty[entry.key]; !ok && isNonEmptyValue(entry.value) {
			firstNonEmpty[entry.key] = i
		}
	}

	chosen := make(map[string]int)
	for key, last := range lastIndex {
		if first, ok := firstNonEmpty[key]; ok {
			chosen[key] = first
		} else {
			chosen[key] = last
		}
	}

	filtered := make([]objectEntry, 0, len(o.entries))
	for i, entry := range o.entries {
		if chosen[entry.key] == i {
			filtered = append(filtered, entry)
		}
	}

	o.entries = filtered
	return o
}

func expandDottedEntries(entries []objectEntry) []objectEntry {
	needsExpand := false
	for _, entry := range entries {
		if strings.Contains(entry.key, ".") {
			needsExpand = true
			break
		}
	}
	if !needsExpand {
		return entries
	}

	expanded := make([]objectEntry, 0, len(entries))
	for _, entry := range entries {
		if !strings.Contains(entry.key, ".") {
			expanded = append(expanded, entry)
			continue
		}

		parts := strings.Split(entry.key, ".")
		if len(parts) == 1 {
			expanded = append(expanded, entry)
			continue
		}

		insertPath(&expanded, parts, entry.value)
	}

	return expanded
}

func insertPath(entries *[]objectEntry, parts []string, value node) {
	if len(parts) == 0 {
		return
	}
	key := parts[0]
	if len(parts) == 1 {
		*entries = append(*entries, objectEntry{key: key, value: value})
		return
	}

	target := findMergeTarget(*entries, key)
	if target == nil {
		target = &objectNode{entries: make([]objectEntry, 0)}
		*entries = append(*entries, objectEntry{key: key, value: target})
	}

	insertIntoObject(target, parts[1:], value)
}

func findMergeTarget(entries []objectEntry, key string) *objectNode {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].key != key {
			continue
		}
		if obj, ok := entries[i].value.(*objectNode); ok {
			return obj
		}
		return nil
	}
	return nil
}

func insertIntoObject(obj *objectNode, parts []string, value node) {
	if len(parts) == 0 {
		return
	}
	key := parts[0]
	if len(parts) == 1 {
		obj.entries = append(obj.entries, objectEntry{key: key, value: value})
		return
	}

	target := findMergeTarget(obj.entries, key)
	if target == nil {
		target = &objectNode{entries: make([]objectEntry, 0)}
		obj.entries = append(obj.entries, objectEntry{key: key, value: target})
	}

	insertIntoObject(target, parts[1:], value)
}

type arrayNode struct {
	values []node
}

func (a *arrayNode) Write(buf *bytes.Buffer) {
	buf.WriteByte('[')
	for i, value := range a.values {
		if i > 0 {
			buf.WriteByte(',')
		}
		value.Write(buf)
	}
	buf.WriteByte(']')
}

func (a *arrayNode) Dedup() node {
	for i := range a.values {
		a.values[i] = a.values[i].Dedup()
	}
	return a
}

func isNonEmptyValue(n node) bool {
	switch v := n.(type) {
	case *valueNode:
		switch v.kind {
		case kindNull:
			return false
		case kindString:
			return v.str != ""
		default:
			return true
		}
	default:
		return true
	}
}

func writeJSONString(buf *bytes.Buffer, s string) {
	encoded, _ := json.Marshal(s)
	buf.Write(encoded)
}

func parseJSON(input string) (node, error) {
	var parser fastjson.Parser
	value, err := parser.Parse(input)
	if err != nil {
		return nil, err
	}

	return convertFastJSON(value)
}

func convertFastJSON(value *fastjson.Value) (node, error) {
	switch value.Type() {
	case fastjson.TypeObject:
		obj, err := value.Object()
		if err != nil {
			return nil, err
		}

		entries := make([]objectEntry, 0)
		obj.Visit(func(key []byte, v *fastjson.Value) {
			child, convErr := convertFastJSON(v)
			if convErr != nil {
				err = convErr
				return
			}
			entries = append(entries, objectEntry{key: string(key), value: child})
		})
		if err != nil {
			return nil, err
		}

		return &objectNode{entries: entries}, nil
	case fastjson.TypeArray:
		values, err := value.Array()
		if err != nil {
			return nil, err
		}

		nodes := make([]node, 0, len(values))
		for _, item := range values {
			child, convErr := convertFastJSON(item)
			if convErr != nil {
				return nil, convErr
			}
			nodes = append(nodes, child)
		}

		return &arrayNode{values: nodes}, nil
	case fastjson.TypeString:
		return &valueNode{kind: kindString, str: string(value.GetStringBytes())}, nil
	case fastjson.TypeNumber:
		return &valueNode{kind: kindNumber, num: value.String()}, nil
	case fastjson.TypeTrue:
		return &valueNode{kind: kindBool, b: true}, nil
	case fastjson.TypeFalse:
		return &valueNode{kind: kindBool, b: false}, nil
	case fastjson.TypeNull:
		return &valueNode{kind: kindNull}, nil
	default:
		return nil, fmt.Errorf("unexpected fastjson type %v", value.Type())
	}
}

func unescapeTSV(input string) (string, error) {
	if strings.IndexByte(input, '\\') == -1 {
		return input, nil
	}

	var out strings.Builder
	out.Grow(len(input))
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if ch != '\\' {
			out.WriteByte(ch)
			continue
		}

		if i+1 >= len(input) {
			return "", fmt.Errorf("trailing backslash in TSV input")
		}

		i++
		next := input[i]
		switch next {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case '0':
			out.WriteByte(0)
		case '\\':
			out.WriteByte('\\')
		default:
			out.WriteByte(next)
		}
	}

	return out.String(), nil
}

func escapeTSV(input string) string {
	needsEscape := false
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '\n', '\t', '\r', '\\', 0, '\b', '\f':
			needsEscape = true
			break
		}
	}

	if !needsEscape {
		return input
	}

	var out strings.Builder
	out.Grow(len(input) + 8)
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '\n':
			out.WriteString("\\n")
		case '\t':
			out.WriteString("\\t")
		case '\r':
			out.WriteString("\\r")
		case '\b':
			out.WriteString("\\b")
		case '\f':
			out.WriteString("\\f")
		case 0:
			out.WriteString("\\0")
		case '\\':
			out.WriteString("\\\\")
		default:
			out.WriteByte(input[i])
		}
	}

	return out.String()
}

func processLine(rawLine string) (string, error) {
	unescaped, err := unescapeTSV(rawLine)
	if err != nil {
		return "", fmt.Errorf("tsv unescape error: %w", err)
	}

	parsed, err := parseJSON(unescaped)
	if err != nil {
		return "", fmt.Errorf("json parse error: %w", err)
	}

	result := parsed.Dedup()
	buf := &bytes.Buffer{}
	result.Write(buf)

	return escapeTSV(buf.String()), nil
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			fmt.Fprintf(os.Stderr, "stdin read error: %v\n", err)
			return
		}

		if len(line) == 0 && err == io.EOF {
			return
		}

		hadNewline := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		output, procErr := processLine(line)
		if procErr != nil {
			fmt.Fprintf(os.Stderr, "line processing error: %v\n", procErr)
			os.Exit(1)
		}

		_, _ = writer.WriteString(output)
		if hadNewline {
			_, _ = writer.WriteString("\n")
		}

		if err == io.EOF {
			return
		}
	}
}
