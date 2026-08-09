package csvcopy

import (
	"fmt"
	"reflect"
	"strings"
)

// binding ties one struct field to one column index.
type binding struct {
	field  int
	column int
}

/*
decodePlan is resolved once per file, which keeps the tag lookups, the map of
column names and the type checks off the row path. Decoding a row is then a walk
over a flat slice of index pairs.
*/
type decodePlan []binding

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
		return nil, fmt.Errorf("%w: %s is not a struct", ErrSchema, structType)
	}

	fields := make([]tagged, 0, structType.NumField())

	for i := range structType.NumField() {
		field := structType.Field(i)

		tag := field.Tag.Get(set.tag)
		if tag == "" || tag == "-" {
			continue
		}

		if !field.IsExported() {
			return nil, fmt.Errorf("%w: %s.%s is tagged %s:%q but is not exported",
				ErrSchema, structType.Name(), field.Name, set.tag, tag)
		}

		// Every column is carried as text and typed later by the convert func,
		// which is the only place that can tell an empty cell from a zero value.
		if field.Type.Kind() != reflect.String {
			return nil, fmt.Errorf("%w: %s.%s is tagged %s:%q but is %s, not a string",
				ErrSchema, structType.Name(), field.Name, set.tag, tag, field.Type)
		}

		fields = append(fields, tagged{field: i, name: set.normalizeHeader(tag)})
	}

	return fields, nil
}

/*
buildPlan matches tags against the header.

Order does not matter and a column no field is tagged for is ignored, so an export
may reorder its columns or add new ones without breaking. A tagged column that the
header does not have is fatal - see ErrMissingColumns.

Also returns the header columns nothing bound to. They are the only visible sign
that an export renamed a column: the tag then simply matches nothing, no error is
raised, and the wrong data reaches the database.
*/
func buildPlan[S any](header []string, set settings) (decodePlan, []string, error) {
	fields, err := taggedFields[S](set)
	if err != nil {
		return nil, nil, err
	}

	// A duplicated column name binds to its first occurrence; the later one is
	// reported as unused rather than silently overriding.
	columns := make(map[string]int, len(header))
	for i, name := range header {
		if _, taken := columns[name]; !taken {
			columns[name] = i
		}
	}

	var missing []string
	bound := make([]bool, len(header))
	plan := make(decodePlan, 0, len(fields))

	for _, field := range fields {
		column, ok := columns[field.name]
		if !ok {
			missing = append(missing, field.name)
			continue
		}

		bound[column] = true
		plan = append(plan, binding{field: field.field, column: column})
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

/*
apply copies one record into the struct.

Every bound field is assigned on every row, so none of them survives from the
previous one. A record that stops short of a bound column leaves that field empty
rather than stale, which matters because the struct is reused across the whole
file.

Fields no column bound to are not touched at all - neither the untagged ones nor
those tagged "-". The struct is the caller's to use as scratch space between rows,
and zeroing it here would take that away without asking.
*/
func (p decodePlan) apply(record []string, dst reflect.Value) {
	for _, b := range p {
		if b.column < len(record) {
			dst.Field(b.field).SetString(record[b.column])
			continue
		}
		dst.Field(b.field).SetString("")
	}
}
