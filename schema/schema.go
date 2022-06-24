package schema

type Vertex interface {
	Label() string
	Properties() map[string]interface{}
	GetBackendMeta() interface{}
	SetBackendMeta(interface{})
}

type Edge struct {
	Source     Vertex
	Label      string
	Target     Vertex
	Properties map[string]interface{}
}

type vertexBase struct {
	backendMeta interface{}
}

func (vb *vertexBase) GetBackendMeta() interface{} {
	return vb.backendMeta
}

func (vb *vertexBase) SetBackendMeta(v interface{}) {
	vb.backendMeta = v
}

func (_ *vertexBase) Properties() map[string]interface{} {
	return map[string]interface{}{}
}

type Package struct {
	vertexBase
	SourceURL string
	Version   string
	Functions []*Function `json:"-"`
}

func (_ *Package) Label() string {
	return "package"
}

func (p *Package) Properties() map[string]interface{} {
	return map[string]interface{}{
		"SourceURL": p.SourceURL,
		"Version":   p.Version,
	}
}

type Function struct {
	vertexBase
	Name           string
	Package        *Package        `json:"-"`
	FirstStatement *Statement      `json:"-"`
	Statements     []*Statement    `json:"-"`
	Calls          []*FunctionCall `json:"-"`
}

func (_ *Function) Label() string {
	return "function"
}

func (f *Function) Properties() map[string]interface{} {
	return map[string]interface{}{
		"Name": f.Name,
	}
}

type Variable struct {
	vertexBase
	Name string
	Type string
}

func (_ *Variable) Label() string {
	return "variable"
}

func (v *Variable) Properties() map[string]interface{} {
	return map[string]interface{}{
		"Name": v.Name,
		"Type": v.Type,
	}
}

type Statement struct {
	vertexBase
	File       string
	Offset     int
	Text       string
	ASTType    string
	Next       []*Statement `json:"-"`
	References []*Variable  `json:"-"`
	Assigns    []*Variable  `json:"-"`
}

func (_ *Statement) Label() string {
	return "statement"
}

func (s *Statement) Properties() map[string]interface{} {
	return map[string]interface{}{
		"File":    s.File,
		"Offset":  s.Offset,
		"Text":    s.Text,
		"ASTType": s.ASTType,
	}
}

type FunctionCall struct {
	vertexBase
	Caller *Function   `json:"-"`
	Callee *Function   `json:"-"`
	Args   []*Variable `json:"-"`
	Return *Variable   `json:"-"`
}

func (_ *FunctionCall) Label() string {
	return "functioncall"
}
