package tlast

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
)

// SchemaIndex is an immutable global type and constructor-tag registry. Updates
// replace only changed documents and affected name/tag buckets; older indexes
// remain safe for concurrent validation.
type SchemaIndex struct {
	documents  map[string]*schemaContributions
	types      [256]map[string]*schemaTypeBucket
	tags       [256]map[uint32]*schemaTagBucket
	typeErrors map[string][]orderedSemanticError
	tagErrors  map[uint32][]orderedSemanticError
	errors     []SemanticError
}

type schemaOrder struct {
	document string
	tl2      bool
	ordinal  int
}

func compareSchemaOrder(a, b schemaOrder) int {
	if a.tl2 != b.tl2 {
		if a.tl2 {
			return 1
		}
		return -1
	}
	if n := cmp.Compare(a.document, b.document); n != 0 {
		return n
	}
	return cmp.Compare(a.ordinal, b.ordinal)
}

type schemaTypeEntry struct {
	order schemaOrder
	typ   *semanticType
}

type schemaTagEntry struct {
	order schemaOrder
	tag   uint32
	name  string
	pr    *PositionRange
}

type schemaContributions struct {
	types []schemaTypeEntry
	tags  []schemaTagEntry
}

type schemaTypeBucket struct{ entries []schemaTypeEntry }
type schemaTagBucket struct{ entries []schemaTagEntry }
type orderedSemanticError struct {
	order schemaOrder
	err   SemanticError
}

// NewSchemaIndex orders documents by ID, keeping TL declarations before TL2 as
// NewSchemaValidator does. Document IDs must remain stable across updates.
func NewSchemaIndex(files map[string]SchemaDeclarations) *SchemaIndex {
	changes := make(map[string]*SchemaDeclarations, len(files))
	for id, file := range files {
		changes[id] = &file
	}
	return emptySchemaIndex().WithDocuments(changes)
}

func emptySchemaIndex() *SchemaIndex {
	return &SchemaIndex{
		documents:  map[string]*schemaContributions{},
		typeErrors: map[string][]orderedSemanticError{},
		tagErrors:  map[uint32][]orderedSemanticError{},
	}
}

func schemaNameShard(name string) uint8 {
	// FNV-1a keeps sequential schema names spread across independent map shards.
	var hash uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		hash = (hash ^ uint32(name[i])) * 16777619
	}
	return uint8(hash ^ hash>>16)
}

func schemaTagShard(tag uint32) uint8 { return uint8(tag ^ tag>>8 ^ tag>>16 ^ tag>>24) }

func (v *SchemaIndex) typeByName(name string) *semanticType {
	if bucket := v.types[schemaNameShard(name)][name]; bucket != nil {
		return bucket.entries[0].typ
	}
	return nil
}

func (v *SchemaIndex) GlobalErrors() []SemanticError { return v.errors }

func (v *SchemaIndex) Validate(file SchemaDeclarations) []SemanticError {
	c := &semanticChecker{index: v}
	return c.validate(file)
}

// WithDocuments returns a new index. A nil value removes a document. All input
// ASTs and returned errors are read-only for the lifetime of their snapshots.
func (v *SchemaIndex) WithDocuments(changes map[string]*SchemaDeclarations) *SchemaIndex {
	if len(changes) == 0 {
		return v
	}
	next := *v
	next.documents = maps.Clone(v.documents)
	next.typeErrors = maps.Clone(v.typeErrors)
	next.tagErrors = maps.Clone(v.tagErrors)
	types := map[string][]schemaTypeEntry{}
	tags := map[uint32][]schemaTagEntry{}
	for id, file := range changes {
		if old := v.documents[id]; old != nil {
			for _, entry := range old.types {
				types[entry.typ.name] = types[entry.typ.name]
			}
			for _, entry := range old.tags {
				tags[entry.tag] = tags[entry.tag]
			}
		}
		delete(next.documents, id)
		if file == nil {
			continue
		}
		contributions := collectSchemaContributions(id, *file)
		next.documents[id] = contributions
		for _, entry := range contributions.types {
			types[entry.typ.name] = append(types[entry.typ.name], entry)
		}
		for _, entry := range contributions.tags {
			tags[entry.tag] = append(tags[entry.tag], entry)
		}
	}
	var changedTypes, changedTags [256]bool
	for name, added := range types {
		shard := schemaNameShard(name)
		if !changedTypes[shard] {
			next.types[shard] = maps.Clone(v.types[shard])
			if next.types[shard] == nil {
				next.types[shard] = map[string]*schemaTypeBucket{}
			}
			changedTypes[shard] = true
		}
		entries := added
		if old := v.types[shard][name]; old != nil {
			for _, entry := range old.entries {
				if _, replaced := changes[entry.order.document]; !replaced {
					entries = append(entries, entry)
				}
			}
		}
		delete(next.typeErrors, name)
		if len(entries) == 0 {
			delete(next.types[shard], name)
			continue
		}
		slices.SortFunc(entries, func(a, b schemaTypeEntry) int { return compareSchemaOrder(a.order, b.order) })
		next.types[shard][name] = &schemaTypeBucket{entries}
		if len(entries) > 1 {
			c := &semanticChecker{types: map[string]*semanticType{name: entries[0].typ}}
			for _, entry := range entries[1:] {
				before := len(c.errors)
				c.add(entry.typ)
				if len(c.errors) > before {
					next.typeErrors[name] = append(next.typeErrors[name], orderedSemanticError{entry.order, c.errors[before]})
				}
			}
		}
	}
	for tag, added := range tags {
		shard := schemaTagShard(tag)
		if !changedTags[shard] {
			next.tags[shard] = maps.Clone(v.tags[shard])
			if next.tags[shard] == nil {
				next.tags[shard] = map[uint32]*schemaTagBucket{}
			}
			changedTags[shard] = true
		}
		entries := added
		if old := v.tags[shard][tag]; old != nil {
			for _, entry := range old.entries {
				if _, replaced := changes[entry.order.document]; !replaced {
					entries = append(entries, entry)
				}
			}
		}
		delete(next.tagErrors, tag)
		if len(entries) == 0 {
			delete(next.tags[shard], tag)
			continue
		}
		slices.SortFunc(entries, func(a, b schemaTagEntry) int { return compareSchemaOrder(a.order, b.order) })
		next.tags[shard][tag] = &schemaTagBucket{entries}
		for i, entry := range entries {
			var err SemanticError
			if tag == 0 {
				err = SemanticError{Code: "crc-zero", Message: "constructor tag 0 is prohibited, even if generated implicitly", Pos: *entry.pr}
			} else if i > 0 {
				err = SemanticError{Code: "crc-conflict", Message: fmt.Sprintf("constructor tag #%08x is used again by %q", tag, entry.name), Pos: *entry.pr, Related: entries[0].pr}
			} else {
				continue
			}
			next.tagErrors[tag] = append(next.tagErrors[tag], orderedSemanticError{entry.order, err})
		}
	}
	next.errors = next.collectErrors()
	return &next
}

func collectSchemaContributions(id string, file SchemaDeclarations) *schemaContributions {
	result := &schemaContributions{}
	for i := range file.TL {
		a := file.TL[i]
		params := tl1Params(a.TemplateArguments)
		order := schemaOrder{document: id, ordinal: 2 * i}
		if !a.Builtin {
			result.types = append(result.types, schemaTypeEntry{order, &semanticType{name: a.Construct.Name.String(), pr: a.Construct.NamePR, params: params, function: a.IsFunction}})
		}
		if !a.IsFunction {
			order.ordinal++
			result.types = append(result.types, schemaTypeEntry{order, &semanticType{name: a.TypeDecl.Name.String(), pr: a.TypeDecl.NamePR, params: params, sharedTL1: true}})
		}
		result.tags = append(result.tags, schemaTagEntry{schemaOrder{document: id, ordinal: i}, a.Crc32(), a.Construct.Name.String(), &a.Construct.IDPR})
	}
	for i := range file.TL2 {
		a := &file.TL2[i]
		order := schemaOrder{document: id, tl2: true, ordinal: i}
		typ := &semanticType{name: a.ReferenceName().String(), pr: a.ReferenceNamePR(), params: a.TypeDecl.TemplateArguments, function: a.IsFunction}
		if !a.IsFunction && a.TypeDecl.Type.IsAlias() {
			typ.alias = &a.TypeDecl.Type.TypeAlias
		}
		result.types = append(result.types, schemaTypeEntry{order, typ})
		if a.IsFunction {
			if a.FuncDecl.Magic != 0 {
				result.tags = append(result.tags, schemaTagEntry{order, a.FuncDecl.Magic, a.FuncDecl.Name.String(), &a.FuncDecl.PRID})
			}
		} else if a.TypeDecl.Magic != 0 {
			result.tags = append(result.tags, schemaTagEntry{order, a.TypeDecl.Magic, a.TypeDecl.Name.String(), &a.TypeDecl.PRID})
		}
	}
	return result
}

func (v *SchemaIndex) collectErrors() []SemanticError {
	var result []SemanticError
	// Match the full validator's ordering: signature errors, then CRC errors;
	// within each pass, TL first and documents/declarations in source order.
	var signatureErrors []orderedSemanticError
	for _, errors := range v.typeErrors {
		signatureErrors = append(signatureErrors, errors...)
	}
	slices.SortFunc(signatureErrors, func(a, b orderedSemanticError) int { return compareSchemaOrder(a.order, b.order) })
	for _, item := range signatureErrors {
		result = append(result, item.err)
	}
	var ordered []orderedSemanticError
	for _, errors := range v.tagErrors {
		ordered = append(ordered, errors...)
	}
	slices.SortFunc(ordered, func(a, b orderedSemanticError) int { return compareSchemaOrder(a.order, b.order) })
	for _, item := range ordered {
		result = append(result, item.err)
	}
	return result
}
