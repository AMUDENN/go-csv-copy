package decode

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

// binding ties one struct field to one column index. Resolved against a type, so
// it is the half of the plan that depends on S and not on any particular value.
type binding struct {
	field  int
	column int
}

/*
slot is a binding resolved against one instance: the field's address, taken once,
so the row path does no reflection at all.
*/
type slot struct {
	dst    *string
	column int
}

/*
decodePlan is resolved once per file, which keeps the tag lookups, the map of
column names and the type checks off the row path. Decoding a row is then a walk
over a flat slice of pointers.
*/
type decodePlan []slot

// tagged is one field that asks for a column, before it is known whether the file
// has one.
type tagged struct {
	field int
	name  string
}

/*
taggedFields collects the fields that ask for a column and rejects a struct that
cannot be decoded into at all.

Kept separate from matching so that these errors surface even for an empty file:
they are bugs in the calling code, and waiting for a file with rows to reveal them
is waiting for production.
*/
func taggedFields[S any](set settings) ([]tagged, error) {
	structType := reflect.TypeFor[S]()
	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: %s is not a struct", csvcopy.ErrSchema, structType)
	}

	fields := make([]tagged, 0, structType.NumField())

	for i := range structType.NumField() {
		field := structType.Field(i)

		tag := field.Tag.Get(set.tag)
		if tag == "" || tag == "-" {
			if field.Anonymous && hasTag(field.Type, set.tag, map[reflect.Type]bool{structType: true}) {
				return nil, fmt.Errorf(
					"%w: %s embeds %s, which has %s tags, and embedded fields are not matched - list the columns on %s itself",
					csvcopy.ErrSchema, structType.Name(), field.Type, set.tag, structType.Name())
			}

			continue
		}

		if !field.IsExported() {
			return nil, fmt.Errorf("%w: %s.%s is tagged %s:%q but is not exported",
				csvcopy.ErrSchema, structType.Name(), field.Name, set.tag, tag)
		}

		// Every column is carried as text and typed later by the convert func,
		// which is the only place that can tell an empty cell from a zero value.
		if field.Type.Kind() != reflect.String {
			return nil, fmt.Errorf("%w: %s.%s is tagged %s:%q but is %s, not a string",
				csvcopy.ErrSchema, structType.Name(), field.Name, set.tag, tag, field.Type)
		}

		name := set.normalizeHeader(tag)

		// The tag == "" check above is on the raw tag, and normalizing happens after
		// it: a tag of nothing but whitespace survives that and arrives here as "".
		// It would then match a header column that normalizes to "" - a binding
		// nobody wrote on purpose, against a column nobody named. WithTag("") is
		// refused for the same reason, one step earlier.
		if name == "" {
			return nil, fmt.Errorf("%w: %s.%s is tagged %s:%q, which normalizes to an empty column name",
				csvcopy.ErrSchema, structType.Name(), field.Name, set.tag, tag)
		}

		// Two fields asking for one column is a copy-paste slip far more often than
		// an intent to duplicate a value, and the reader cannot tell the two apart.
		// A linear scan beats a map: a struct has a handful of tagged fields, and
		// this way the check costs no allocation.
		for _, existing := range fields {
			if existing.name == name {
				return nil, fmt.Errorf("%w: %s.%s and %s.%s both ask for column %q",
					csvcopy.ErrSchema, structType.Name(), structType.Field(existing.field).Name,
					structType.Name(), field.Name, name)
			}
		}

		fields = append(fields, tagged{field: i, name: name})
	}

	// A struct with no tag at all is ErrMissingColumns taken to its limit: there,
	// one field binds to nothing and reads as empty on every row; here, every field
	// does. Nothing would object - Rows counts the file's rows, Err stays nil, and
	// CopyFrom writes a full table of empty values, with Unused holding the whole
	// header as the only trace. `json:"..."` where `csv:"..."` was meant is the way
	// this happens.
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: %s has no %s tags", csvcopy.ErrSchema, structType.Name(), set.tag)
	}

	return fields, nil
}

/*
hasTag reports whether t, or anything t embeds, carries the tag.

Only used to refuse such a struct. Walking into an embedded struct and decoding
into it would be a feature; doing neither is the bug this guards against - the
tag binds to nothing, the field reads as empty on every row, and the column it
was meant to fill is wiped with NULL. That is what ErrMissingColumns exists to
prevent, and it cannot see this case because the tag never reaches the header
matcher at all.

visited stops a struct that embeds itself through a pointer from recursing
forever.
*/
func hasTag(t reflect.Type, tag string, visited map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return false
	}
	visited[t] = true

	for i := range t.NumField() {
		field := t.Field(i)

		if value := field.Tag.Get(tag); value != "" && value != "-" {
			return true
		}
		if field.Anonymous && hasTag(field.Type, tag, visited) {
			return true
		}
	}

	return false
}

/*
buildPlan matches tags against the header.

Order does not matter and a column no field is tagged for is ignored, so an export
may reorder its columns or add new ones without breaking. A tagged column that the
header does not have is fatal - see ErrMissingColumns.

Also returns the header columns nothing bound to. They are the only visible sign
that an export renamed a column: the tag then simply matches nothing, no error is
raised, and the wrong data reaches the database.

A tag that matches a name the header carries more than once is fatal too, for the
mirror image of the reason two fields asking for one column are - see
ErrDuplicateColumns.
*/
func buildPlan[S any](header []string, set settings) ([]binding, []string, error) {
	fields, err := taggedFields[S](set)
	if err != nil {
		return nil, nil, err
	}

	// A duplicated column name binds to its first occurrence; the later one is
	// reported as unused rather than silently overriding. Which occurrence that is
	// is decided by position, so it is only tolerable while no tag asks for the
	// name - see ErrDuplicateColumns.
	columns := make(map[string]int, len(header))
	duplicates := make(map[string][]int)

	for i, name := range header {
		if first, taken := columns[name]; taken {
			if len(duplicates[name]) == 0 {
				duplicates[name] = []int{first}
			}
			duplicates[name] = append(duplicates[name], i)

			continue
		}
		columns[name] = i
	}

	var missing, ambiguous []string
	bound := make([]bool, len(header))
	plan := make([]binding, 0, len(fields))

	for _, field := range fields {
		column, ok := columns[field.name]
		if !ok {
			missing = append(missing, field.name)
			continue
		}
		if at := duplicates[field.name]; len(at) > 0 {
			ambiguous = append(ambiguous, fmt.Sprintf("%q (columns %s)", field.name, positions(at)))
			continue
		}

		bound[column] = true
		plan = append(plan, binding{field: field.field, column: column})
	}

	// Checked before the missing ones: a header this ambiguous is not a header the
	// caller can act on a list of missing names from.
	if len(ambiguous) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrDuplicateColumns, strings.Join(ambiguous, ", "))
	}

	// Every missing column is reported at once: one run tells the whole story of
	// how far the export has drifted, instead of one name per redeploy.
	if len(missing) > 0 && !set.allowMissing {
		return nil, nil, fmt.Errorf("%w: %s", ErrMissingColumns, strings.Join(missing, ", "))
	}

	var unused []string
	for i, used := range bound {
		if !used {
			unused = append(unused, header[i])
		}
	}

	return plan, unused, nil
}

// positions renders column indexes as the 1-based numbers someone counting
// columns in the file arrives at.
func positions(at []int) string {
	numbers := make([]string, len(at))
	for i, column := range at {
		numbers[i] = strconv.Itoa(column + 1)
	}

	return strings.Join(numbers, ", ")
}

/*
apply copies one record into the struct, reporting whether the record ran out
before a bound column.

Every bound field is assigned on every row, so none of them survives from the
previous one. A record that stops short of a bound column leaves that field empty
rather than stale, which matters because the struct is reused across the whole file.

This is where Typed and Raw genuinely differ, and the difference is not cosmetic.
Raw deals in []any and can put a nil there, so a column the record never reached
becomes SQL NULL. A field here is declared string; there is no nil to assign, so it
becomes "". In Postgres a NULL and an empty string are different values, and only
convert could tell the two cases apart - which is what the returned bool is for.

Fields no column bound to are not touched at all - neither the untagged ones nor
those tagged "-". The struct is the caller's to use as scratch space between rows,
and zeroing it here would take that away without asking.
*/
func (p decodePlan) apply(record []string) bool {
	truncated := false

	for _, s := range p {
		if s.column < len(record) {
			*s.dst = record[s.column]

			continue
		}
		*s.dst = ""
		truncated = true
	}

	return truncated
}

/*
bindPlan resolves a plan against the struct it will decode into.

Taking the addresses once is what keeps reflect off the row path: Field plus
SetString costs about five times a pointer write, and on a wide file that is most
of what separated Typed from Raw. It is safe for the same reason caching the
reflect.Value was - dst is a field of a heap-allocated source, and its address does
not move.

What it does cost is that the source can no longer be copied by value: the plan
would keep writing into the original's struct while convert read the copy's. See
noCopy, which is there to make go vet say so.

The type assertion cannot fail. taggedFields has already refused every field that
is not an exported string, and buildPlan only emits bindings for fields it
returned.
*/
func bindPlan(bindings []binding, dst reflect.Value) decodePlan {
	plan := make(decodePlan, len(bindings))
	for i, b := range bindings {
		plan[i] = slot{
			//nolint:errcheck,forcetypeassert // taggedFields refused every field that is not an exported string, and buildPlan only emits bindings for fields it returned; a comma-ok here would be a branch no input can reach
			dst:    dst.Field(b.field).Addr().Interface().(*string),
			column: b.column,
		}
	}

	return plan
}
