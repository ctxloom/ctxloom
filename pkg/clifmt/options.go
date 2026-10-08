package clifmt

import (
	"errors"
	"fmt"
	"io"
	"reflect"
)

// Option customises rendering. Options passed to Render (or Printer.Render)
// apply to that one call; options passed to New apply to every call through
// the Printer. Only ViewFor may be given to New: WithWriter and At address
// one call's value.
//
// Custom views shape the human formats (text, markdown) only. json, yaml and
// toml always come from the json contract, so a view can never drift the
// machine shape; a type changes that with json.Marshaler, a call with
// WithWriter.
type Option func(*config)

// viewFn is a custom view over one node of the walk. v is the node's
// dereferenced value (invalid for nil).
type viewFn func(c *ViewCtx, v reflect.Value) (Doc, error)

// config collects one scope's options. Conflicts are recorded, not
// resolved: "last wins" would hide a mistake.
type config struct {
	printerScope bool
	writers      map[Format]func(io.Writer) error
	paths        map[string]viewFn
	types        map[reflect.Type]viewFn
	errs         []error
}

func newConfig(printerScope bool, opts []Option) (*config, error) {
	c := &config{
		printerScope: printerScope,
		writers:      map[Format]func(io.Writer) error{},
		paths:        map[string]viewFn{},
		types:        map[reflect.Type]viewFn{},
	}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c, errors.Join(c.errs...)
}

func (c *config) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("clifmt: "+format, args...))
}

// WithWriter writes format f with fn instead of rendering the value: the
// narrowest escape hatch, raw bytes for one format of one call. It pre-empts
// every view and the derivation for f, and leaves every other format as it
// was. It may target any format, structured ones included (a streaming or
// JSON-Lines command); it is per call only, never on a Printer.
func WithWriter(f Format, fn func(w io.Writer) error) Option {
	return func(c *config) {
		switch {
		case c.printerScope:
			c.fail("WithWriter(%s) applies to one call; pass it to Render, not New", f)
		case !f.Valid():
			c.errs = append(c.errs, UnsupportedFormatError(string(f)))
		case fn == nil:
			c.fail("WithWriter(%s) given a nil func", f)
		default:
			if _, dup := c.writers[f]; dup {
				c.fail("WithWriter(%s) given twice in one call", f)
				return
			}
			c.writers[f] = fn
		}
	}
}

// At replaces or decorates the human view of the node at path, for one call.
// path is dotted json names from the rendered value: "" is the value itself,
// "owner.name" a nested field, "rows[]" every element of a list and
// "rows[].env" a field of every element. A path is checked against the
// value's static type before rendering, so one that names no field is an
// error rather than an override that silently never fires.
//
// fn returns the blocks that stand in for the node's derived blocks in its
// parent; c.Derived() yields those, for decorating. A nil Doc hides the
// node. v is the node's value with pointers removed (nil for a nil pointer).
func At(path string, fn func(c *ViewCtx, v any) (Doc, error)) Option {
	return func(c *config) {
		switch {
		case c.printerScope:
			c.fail("At(%q) addresses one call's value; pass it to Render, not New", path)
		case fn == nil:
			c.fail("At(%q) given a nil func", path)
		default:
			if _, dup := c.paths[path]; dup {
				c.fail("At(%q) given twice in one call", path)
				return
			}
			c.paths[path] = func(ctx *ViewCtx, v reflect.Value) (Doc, error) {
				var arg any
				if v.IsValid() && v.CanInterface() {
					arg = v.Interface()
				}
				return fn(ctx, arg)
			}
		}
	}
}

// ViewFor replaces or decorates the human view of every value of type T,
// wherever it appears in the rendered tree (the root, a field, a list
// element, a table cell). It may be given per call or to New for every call.
// T must be a concrete, non-pointer type; a *T value matches too.
func ViewFor[T any](fn func(c *ViewCtx, v T) (Doc, error)) Option {
	t := reflect.TypeFor[T]()
	return func(c *config) {
		switch {
		case t.Kind() == reflect.Interface:
			c.fail("ViewFor[%s]: an interface type never matches; register the concrete types", t)
		case t.Kind() == reflect.Pointer:
			c.fail("ViewFor[%s]: register the element type %s; pointers match it", t, t.Elem())
		case fn == nil:
			c.fail("ViewFor[%s] given a nil func", t)
		default:
			if _, dup := c.types[t]; dup {
				c.fail("ViewFor[%s] given twice in one scope", t)
				return
			}
			c.types[t] = func(ctx *ViewCtx, v reflect.Value) (Doc, error) {
				return fn(ctx, v.Interface().(T))
			}
		}
	}
}

// Printer holds options that apply to every call: the per-type views a
// binary registers once at its composition root. Its zero value is ready to
// use, and it is safe for concurrent use.
type Printer struct {
	types map[reflect.Type]viewFn
}

// New builds a Printer. Two registrations for the same type are an error.
func New(opts ...Option) (*Printer, error) {
	c, err := newConfig(true, opts)
	if err != nil {
		return nil, err
	}
	return &Printer{types: c.types}, nil
}

// Render writes v to w in format f with the Printer's options and the
// call's own. See the package-level Render.
func (p *Printer) Render(w io.Writer, v any, f Format, opts ...Option) error {
	call, err := newConfig(false, opts)
	if err != nil {
		return err
	}
	if fn, ok := call.writers[f]; ok {
		return fn(w)
	}
	spec, ok := specFor(f)
	if !ok {
		return UnsupportedFormatError(string(f))
	}
	if err := validatePaths(reflect.TypeOf(v), call.paths); err != nil {
		return err
	}
	if spec.structured {
		return spec.marshal(w, v)
	}
	d := &deriver{format: f, paths: call.paths, callTypes: call.types}
	if p != nil {
		d.printerTypes = p.types
	}
	doc, err := d.root(v)
	if err != nil {
		return err
	}
	if len(doc) == 0 {
		// An empty view (an all-omitempty struct, an empty list of scalars,
		// a view that hid everything) would otherwise write zero bytes,
		// indistinguishable from "the command produced no output at all"
		// (json/yaml render the same value as `{}`/`null`).
		doc = Doc{Para("(none)")}
	}
	return spec.write(w, doc)
}

// ViewCtx is what a custom view receives: where the node sits and a way to
// reach the view it is replacing or decorating.
type ViewCtx struct {
	d    *deriver
	n    nodeRef
	next []viewFn
}

// Format is the format being rendered: FormatText or FormatMarkdown.
func (c *ViewCtx) Format() Format { return c.d.format }

// Path is the json path of this node, "" at the root.
func (c *ViewCtx) Path() string { return c.n.path }

// Label is the label the node would have been derived with ("" at the root
// and for list items).
func (c *ViewCtx) Label() string { return c.n.label }

// Derived returns the node's view from the next lower layer: the next
// matching view in precedence order, or else clifmt's own derivation. Only
// this node skips the current layer; its children are still rendered with
// every view active.
func (c *ViewCtx) Derived() (Doc, error) { return c.d.eval(c.n, c.next) }

// View derives an arbitrary value as a fresh root, with this call's type
// views applied (path views address the call's own value, so they do not
// apply). Use it to compose a view from other values.
func (c *ViewCtx) View(v any) (Doc, error) {
	sub := &deriver{format: c.d.format, callTypes: c.d.callTypes, printerTypes: c.d.printerTypes}
	return sub.root(v)
}
