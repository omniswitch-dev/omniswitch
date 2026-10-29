package policy

import (
	"encoding/json"
	"sort"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"

	"github.com/omniswitch-dev/omniswitch/internal/adapter/mcp"
)

// argsValue converts decoded tool-call arguments into the CEL value bound
// to the args variable.
//
// Argument names are matched regardless of case at every nesting level:
// MCP servers written in Go commonly decode arguments into structs with
// encoding/json, which matches member names case-insensitively, so a rule on
// args.table must also see an argument sent as "Table". Lookups (field
// selection, indexing, has(), `in`) try the exact name first and then the
// name folded with mcp.FoldName, the folding the MCP adapter uses for the
// derived fields (resource.name, environment, action). size(), iteration
// and the reported keys still see only the original argument names.
//
// json.Number values (from decoders using UseNumber) become int64 when they
// are exact integers and float64 otherwise.
func argsValue(args map[string]any) ref.Val {
	return newFoldedMap(args)
}

// foldedMap is a CEL map with string keys whose key lookups fall back to a
// case-folded match. It wraps a regular CEL map, so iteration, size() and
// conversion behave exactly like a map holding only the original keys.
type foldedMap struct {
	traits.Mapper
	// folded maps the folded form of a key to the original key. When
	// several original keys fold to the same name (mcp.Parse rejects such
	// arguments, but a direct caller of the engine may pass them), the
	// lexicographically smallest original key wins, so the result is
	// deterministic; an exact match is always preferred over this index.
	folded map[string]string
}

func newFoldedMap(values map[string]any) *foldedMap {
	entries := make(map[ref.Val]ref.Val, len(values))
	keys := make([]string, 0, len(values))
	for key, value := range values {
		entries[types.String(key)] = argValue(value)
		keys = append(keys, key)
	}
	sort.Strings(keys)
	folded := make(map[string]string, len(keys))
	for _, key := range keys {
		name := mcp.FoldName(key)
		if _, exists := folded[name]; !exists {
			folded[name] = key
		}
	}
	return &foldedMap{
		Mapper: types.NewRefValMap(types.DefaultTypeAdapter, entries),
		folded: folded,
	}
}

func argValue(value any) ref.Val {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return types.Int(integer)
		}
		if float, err := typed.Float64(); err == nil {
			return types.Double(float)
		}
		return types.String(typed.String())
	case map[string]any:
		return newFoldedMap(typed)
	case []any:
		items := make([]ref.Val, len(typed))
		for index, item := range typed {
			items[index] = argValue(item)
		}
		return types.NewRefValList(types.DefaultTypeAdapter, items)
	case nil:
		return types.NullValue
	default:
		return types.DefaultTypeAdapter.NativeToValue(typed)
	}
}

// Find implements traits.Mapper: the exact key first, then the folded key.
func (m *foldedMap) Find(key ref.Val) (ref.Val, bool) {
	value, found := m.Mapper.Find(key)
	if found {
		return value, true
	}
	name, ok := key.(types.String)
	if !ok {
		return value, found
	}
	original, ok := m.folded[mcp.FoldName(string(name))]
	if !ok {
		return value, found
	}
	return m.Mapper.Find(types.String(original))
}

// Get implements traits.Indexer using the folded lookup.
func (m *foldedMap) Get(key ref.Val) ref.Val {
	value, found := m.Find(key)
	if !found {
		return types.ValOrErr(value, "no such key: %v", key)
	}
	return value
}

// Contains implements traits.Container (the `in` operator) using the
// folded lookup.
func (m *foldedMap) Contains(key ref.Val) ref.Val {
	_, found := m.Find(key)
	return types.Bool(found)
}

// Equal compares with another map. Sizes count only original keys; each key
// of the other map is looked up here with the folded lookup, which matches
// what a plain CEL map does when args is on the right-hand side, so
// equality gives the same answer in both operand orders.
func (m *foldedMap) Equal(other ref.Val) ref.Val {
	otherMap, ok := other.(traits.Mapper)
	if !ok {
		return types.False
	}
	if m.Size() != otherMap.Size() {
		return types.False
	}
	it := otherMap.Iterator()
	for it.HasNext() == types.True {
		key := it.Next()
		otherValue, _ := otherMap.Find(key)
		value, found := m.Find(key)
		if !found {
			return types.False
		}
		if types.Equal(value, otherValue) == types.False {
			return types.False
		}
	}
	return types.True
}
