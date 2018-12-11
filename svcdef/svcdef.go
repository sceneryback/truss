/*
Package svcdef provides a straightforward view of the Go code for a gRPC
service defined using protocol buffers. Information is distilled from the
.proto files comprising the definition, as well as the Go source files
generated from those same .proto files.

Since svcdef is only meant to be used to generate Go code, svcdef has a limited
view of the definition of the gRPC service.

Additionally, since svcdef only parses Go code generated by protoc-gen-go, all
methods accept only ast types with structures created by protoc-gen-go. See
NewTYPE functions such as NewMap for details on the relevant conventions.

Note that svcdef does not support embedding sub-fields of nested messages into
the path of an HTTP annotation.
*/
package svcdef

import (
	"fmt"
	"github.com/serenize/snaker"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/pkg/errors"
)

// Svcdef is the top-level struct for the definition of a service.
type Svcdef struct {
	// PkgName will be the pacakge name of the go file(s) analyzed. So if a
	// Go file contained "package authz", then PkgName will be "authz". If
	// multiple Go files are analyzed, it will be the package name of the last
	// go file analyzed.
	PkgName  string
	PbPkgName string
	Messages []*Message
	Enums    []*Enum
	// Service contains the sole service for this Svcdef
	Service *Service
}

// Message represents a protobuf Message, though greatly simplified.
type Message struct {
	Name   string
	Fields []*Field
}

type Enum struct {
	Name string
}

type Map struct {
	// KeyType will always be a basetype, e.g. string, int64, etc.
	KeyType   *FieldType
	ValueType *FieldType
}

type Service struct {
	Name    string
	Methods []*ServiceMethod
}

type ServiceMethod struct {
	Name         string
	SnakeName 	 string
	RequestType  *FieldType
	ResponseType *FieldType
	// Bindings contains information for mapping http paths and paramters onto
	// the fields of this ServiceMethods RequestType.
	Bindings []*HTTPBinding
}

// Field represents a field on a protobuf message.
type Field struct {
	Name string
	// PBFieldName is the string value of the field name in the definition .proto file.
	// For Example: 'snake_case' from below -- where Name would be 'SnakeCase'
	// `protobuf:"varint,1,opt,name=snake_case,json=snakeCase" json:"snake_case,omitempty"`
	PBFieldName string
	Type        *FieldType
}

// FieldType contains information about the type of one Field on a message,
// such as if that Field is a slice or if it's a pointer, as well as a
// reference to the definition of the type of this Field.
type FieldType struct {
	// Name will contain the name of the type, for example "string" or "bool"
	Name string
	// Enum contains a pointer to the Enum type this fieldtype represents, if
	// this FieldType represents an Enum. If not, Enum is nil.
	Enum *Enum
	// Message contains a pointer to the Message type this FieldType
	// represents, if this FieldType represents a Message. If not, Message is
	// nil.
	Message *Message
	// Map contains a pointer to the Map type this FieldType represents, if
	// this FieldType represents a Map. If not, Map is nil.
	Map *Map
	// StarExpr is True if this FieldType represents a pointer to a type.
	StarExpr bool
	// ArrayType is True if this FieldType represents a slice of a type.
	ArrayType bool
}

// HTTPBinding represents one of potentially several bindings from a gRPC
// service method to a particuar HTTP path/verb.
type HTTPBinding struct {
	Verb string
	Path string
	// There is one HTTPParamter for each of the fields on parent service
	// methods RequestType.
	Params []*HTTPParameter
}

// HTTPParameter represents the location of one field for a given HTTPBinding.
// Each HTTPParameter corresponds to one Field of the parent
// ServiceMethod.RequestType.Fields
type HTTPParameter struct {
	// Field points to a Field on the Parent service methods "RequestType".
	Field *Field
	// Location will be either "body", "path", or "query"
	Location string
}

func retrieveTypeSpecs(f *ast.File) ([]*ast.TypeSpec, error) {
	var rv []*ast.TypeSpec
	for _, dec := range f.Decls {
		switch gendec := dec.(type) {
		case *ast.GenDecl:
			for _, spec := range gendec.Specs {
				switch ts := spec.(type) {
				case *ast.TypeSpec:
					rv = append(rv, ts)
				}
			}
		}
	}
	return rv, nil
}

// DebugInfo contains context necessary for many functions to provide useful
// debugging output when encountering errors. All DebugInfo methods are
// implemented such that calling them with `nil` recievers means they'll return
// empty values. And since DebugInfo is used PURELY for creating nice error
// messages and no actual business logic depends on them, this means you can
// safely pass 'nil' DebugInfo structs to functions and you'll just get
// unhelpful error messages out, it won't break things. This is done to make
// the code more testable.
type DebugInfo struct {
	Fset *token.FileSet
	Path string
}

func (di *DebugInfo) Position(pos token.Pos) string {
	if di == nil {
		return ""
	} else {
		return fmt.Sprintf("%s", di.Fset.Position(pos))
	}
}

// LocationError is a special kind of error, carrying special information about
// where the error was encountered within a file.
type LocationError struct {
	Path     string
	Position string
	Err      string
}

func NewLocationError(err string, path string, pos string) LocationError {
	return LocationError{
		Path:     path,
		Position: pos,
		Err:      err,
	}
}

func (le LocationError) Error() string {
	return fmt.Sprintf("%s in file %q at line %s", le.Err, le.Path, le.Position)
}

func (le LocationError) Location() string {
	return le.Position
}

// New creates a Svcdef by parsing the provided Go and Protobuf source files to
// derive type information, gRPC service data, and HTTP annotations.
func New(goFiles map[string]io.Reader, protoFiles map[string]io.Reader) (*Svcdef, error) {
	rv := Svcdef{}

	for path, gofile := range goFiles {
		fset := token.NewFileSet()
		fileAst, err := parser.ParseFile(fset, "", gofile, parser.ParseComments)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot parse go file %q to create Svcdef", path)
		}
		debugInfo := &DebugInfo{
			Path: path,
			Fset: fset,
		}
		rv.PkgName = fileAst.Name.Name

		typespecs, err := retrieveTypeSpecs(fileAst)
		if err != nil {
			return nil, errors.Wrap(err, "cannot retrive type specs")
		}
		for _, t := range typespecs {
			switch typdf := t.Type.(type) {
			case *ast.Ident:
				if typdf.Name == "int32" {
					nenm, err := NewEnum(t)
					if err != nil {
						return nil, errors.Wrapf(err, "error parsing enum %q", t.Name.Name)
					}
					rv.Enums = append(rv.Enums, nenm)
				}
			case *ast.StructType:
				// Non-exported structs do not represent types
				if !t.Name.IsExported() {
					break
				}
				nmsg, err := NewMessage(t)
				if err != nil {
					return nil, errors.Wrapf(err, "error parsing message %q", t.Name.Name)
				}
				rv.Messages = append(rv.Messages, nmsg)
			case *ast.InterfaceType:
				// Each service will have two interfaces ("{SVCNAME}Server" and
				// "{SVCNAME}Client") each containing the same information that we
				// care about, but structured a bit differently. Additionally,
				// oneof fields generate an interface which is not a service - so
				// for simplicity, only process the "Server" interface.
				if !strings.HasSuffix(t.Name.Name, "Server") {
					if !strings.HasSuffix(t.Name.Name, "Client") {
						// This interface isn't either Server or Client; it may be a oneof
						// field, which isn't currently supported.  Warn the user and skip.
						log.Warnf("Unexpected interface %s found; skipping", t.Name.Name)
					}
					break
				}
				nsvc, err := NewService(t, debugInfo)
				if err != nil {
					return nil, errors.Wrapf(err, "error parsing service %q", t.Name.Name)
				}
				rv.Service = nsvc
			}
		}
	}
	resolveTypes(&rv)
	err := consolidateHTTP(&rv, protoFiles)
	if err != nil {
		return nil, errors.Wrap(err, "failed to consolidate HTTP")
	}

	return &rv, nil
}

func NewEnum(e *ast.TypeSpec) (*Enum, error) {
	return &Enum{
		Name: e.Name.Name,
	}, nil
}

// NewMessage returns a new Message struct derived from an *ast.TypeSpec with a
// Type of *ast.StructType.
func NewMessage(m *ast.TypeSpec) (*Message, error) {
	rv := &Message{
		Name: m.Name.Name,
	}

	strct := m.Type.(*ast.StructType)
	for _, f := range strct.Fields.List {
		if strings.HasPrefix(f.Names[0].Name, "XXX_") {
			continue
		}
		nfield, err := NewField(f)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot create field %q while creating message %q", f.Names[0].Name, rv.Name)
		}
		rv.Fields = append(rv.Fields, nfield)
	}

	return rv, nil
}

// NewMap returns a new Map struct derived from an ast.Expr interface
// implemented by an *ast.MapType struct. This code cannot accept an arbitrary
// MapType, only one which follows the conventions of Go code generated by
// protoc-gen-go. Those conventions are:
//
//     1. The KeyType of the *ast.MapType will always be an ast.Ident
//     2. The ValueType may be an ast.Ident OR an ast.StarExpr -> ast.Ident
//
// These rules are a result of the rules for map fields of Protobuf messages,
// namely that a key may only be represented by a non-float basetype (e.g.
// int64, string, etc.), and that a value may be either a basetype or a Message
// type or an Enum type. In the resulting Go code, a basetype will be
// represented as an ast.Ident, while a key that is a Message or Enum type will
// be represented as an *ast.StarExpr which references an ast.Ident.
func NewMap(m ast.Expr) (*Map, error) {
	rv := &Map{
		KeyType:   &FieldType{},
		ValueType: &FieldType{},
	}
	mp := m.(*ast.MapType)
	// KeyType will always be an ast.Ident, ValueType may be an ast.Ident or an
	// ast.StarExpr->ast.Ident
	key := mp.Key.(*ast.Ident)
	rv.KeyType.Name = key.Name
	var keyFollower func(ast.Expr)
	keyFollower = func(e ast.Expr) {
		switch ex := e.(type) {
		case *ast.Ident:
			rv.ValueType.Name = ex.Name
		case *ast.StarExpr:
			rv.ValueType.StarExpr = true
			keyFollower(ex.X)
		}
	}
	keyFollower(mp.Value)

	return rv, nil
}

// NewService returns a new Service struct derived from an *ast.TypeSpec with a
// Type of *ast.InterfaceType representing an "{SVCNAME}Server" interface.
func NewService(s *ast.TypeSpec, info *DebugInfo) (*Service, error) {
	rv := &Service{
		Name: strings.TrimSuffix(s.Name.Name, "Server"),
	}
	asvc := s.Type.(*ast.InterfaceType)
	for _, m := range asvc.Methods.List {
		nmeth, err := NewServiceMethod(m, info)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot create service method %q of service %q", m.Names[0].Name, rv.Name)
		}
		rv.Methods = append(rv.Methods, nmeth)
	}
	return rv, nil
}

// NewServiceMethod returns a new ServiceMethod derived from a method of a
// Service interface. This is accepted in the form of an *ast.Field which
// contains the name of the method.
func NewServiceMethod(m *ast.Field, info *DebugInfo) (*ServiceMethod, error) {
	rv := &ServiceMethod{
		Name: m.Names[0].Name,
		SnakeName: snaker.CamelToSnake(m.Names[0].Name),
	}
	ft, ok := m.Type.(*ast.FuncType)
	if !ok {
		return nil, NewLocationError("provided *ast.Field.Type is not of type "+
			"*ast.FuncType; cannot proceed",
			info.Path, info.Position(m.Pos()))
	}

	input := ft.Params.List
	output := ft.Results.List

	// Zero'th param of a serverMethod is Context.context, while first param is
	// this methods RequestType. Example:
	//
	//     GetMap(context.Context, *MapTypeRequest) (*MapTypeResponse, error)
	//                              └────────────┘    └─────────────┘
	//                                RequestType       ResponseType
	//            └──────────────────────────────┘   └─────────────────────┘
	//                         input                         output

	rq := input[1]
	rs := output[0]

	makeFieldType := func(in *ast.Field) (*FieldType, error) {
		star, ok := in.Type.(*ast.StarExpr)
		if !ok {
			return nil, NewLocationError("cannot create FieldType, in.Type "+
				"is not *ast.StarExpr",
				info.Path, info.Position(in.Pos()))
		}
		var ident *ast.Ident
		ident, ok = star.X.(*ast.Ident)
		if !ok {
			expr, ok := star.X.(*ast.SelectorExpr)
			if !ok {
				return nil, NewLocationError("cannot create FieldType, "+
					"star.Type is not *ast.Ident",
					info.Path, info.Position(star.Pos()))
			}
			ident = expr.Sel
		}
		return &FieldType{
			Name:     ident.Name,
			StarExpr: true,
		}, nil
	}

	var err error
	rv.RequestType, err = makeFieldType(rq)
	if err != nil {
		return nil, errors.Wrapf(err, "requestType creation of service method %q failed", rv.Name)
	}
	rv.ResponseType, err = makeFieldType(rs)
	if err != nil {
		return nil, errors.Wrapf(err, "responseType creation of service method %q failed", rv.Name)
	}

	return rv, nil
}

// NewField returns a Field struct with information distilled from an
// *ast.Field. If the provided *ast.Field does not match the conventions of
// code generated by protoc-gen-go, an error will be returned.
func NewField(f *ast.Field) (*Field, error) {
	// The following is an informational table of how the proto-to-go
	// concepts map to the Types of an ast.Field. An arrow indicates "nested
	// within". This is here as an implementors aid.
	//
	//     | Type Genres | Repeated               | Naked         |
	//     |-------------|------------------------|---------------|
	//     | Enum        | Array -> Ident         | Ident         |
	//     | Message     | Array -> Star -> Ident | Star -> Ident |
	//     | BaseType    | Array -> Ident         | Ident         |
	//
	// Map types will always have a KeyType which is ident, and a value that is one of
	// the Type Genres specified in the table above.
	rv := &Field{
		Name: f.Names[0].Name,
		Type: &FieldType{},
	}

	// TypeFollower 'follows' the type of the provided ast.Field, determining
	// the name of this fields type and if it's a StarExpr, an ArrayType, or
	// both, and modifying the return value accordingly.
	var typeFollower func(ast.Expr) error
	typeFollower = func(e ast.Expr) error {
		if f.Tag != nil {
			tag := reflect.StructTag(f.Tag.Value).Get("json")
			if idx := strings.Index(tag, ","); idx != -1 {
				rv.PBFieldName = tag[:idx]
			}
		}

		switch ex := e.(type) {
		case *ast.Ident:
			rv.Type.Name += ex.Name
		case *ast.StarExpr:
			rv.Type.StarExpr = true
			typeFollower(ex.X)
		case *ast.ArrayType:
			// Handle multi-nested slices, such as repeated bytes, which maps to [][]byte
			if rv.Type.ArrayType {
				rv.Type.Name = "[]" + rv.Type.Name
			}
			rv.Type.ArrayType = true
			typeFollower(ex.Elt)
		case *ast.MapType:
			mp, err := NewMap(ex)
			if err != nil {
				return errors.Wrapf(err, "failed to create map for field %q", rv.Name)
			}
			rv.Type.Map = mp
		case *ast.SelectorExpr:
			rv.Type.Name += ex.Sel.Name
		}

		return nil
	}
	err := typeFollower(f.Type)
	if err != nil {
		return nil, err
	}
	return rv, nil
}
