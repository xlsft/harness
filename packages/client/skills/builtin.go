package skills

import _ "embed"

// SourceBuiltin is a skill that ships inside the client. It ranks last, so anything of the same name overrides it.
const SourceBuiltin = "builtin"

//go:embed builtin_documents.md
var builtinDocuments string

// builtin adds the skills that ship with the client. Only their names and descriptions cost anything until one is loaded.
func (sc *scanner) builtin() {
	s := Parse(builtinDocuments, "documents", KindSkill)
	s.Source, s.Path = SourceBuiltin, "builtin:documents"
	s.doc = docBody{content: builtinDocuments, id: "builtin:documents"}
	sc.add(s)
}
