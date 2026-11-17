package driver

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// The SEC-01 source rules. The guard over the real tree and the self-test over violation snippets both call
// dockerGuardFindings, so they cannot drift apart.
const (
	ruleNetDial      = "net-dial"      // net.Dial*, net.Dialer
	ruleMethodDial   = "method-dial"   // any .Dial / .DialContext call
	ruleHTTPLiteral  = "http-literal"  // http.Transport / http.Client composite literal
	ruleHTTPNew      = "http-new"      // new(http.Transport) / new(http.Client)
	ruleHTTPDefault  = "http-default"  // http.DefaultTransport / http.DefaultClient
	ruleDockerDialer = "docker-dialer" // any reference to dockerDialer
	ruleRawConnect   = "raw-connect"   // unix.Connect / syscall.Connect
	ruleFileConn     = "file-conn"     // net.FileConn / net.FilePacketConn
)

const (
	pkgNet     = "net"
	pkgHTTP    = "net/http"
	pkgUnix    = "golang.org/x/sys/unix"
	pkgSyscall = "syscall"
)

type guardFinding struct {
	rule string
	decl string // the top-level declaration the finding sits in
	text string
	pos  token.Pos
}

type guardRef struct{ path, name string }

// guardImports maps each local name to its import path for the packages the rules care about, and lists the
// packages imported with a dot. Aliases are resolved here, so a rule matches on the package path alone.
func guardImports(file *ast.File) (named map[string]string, dotted []string) {
	named = map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		switch path {
		case pkgNet, pkgHTTP, pkgUnix, pkgSyscall:
		default:
			continue
		}
		switch {
		case spec.Name == nil:
			named[path[strings.LastIndex(path, "/")+1:]] = path
		case spec.Name.Name == ".":
			dotted = append(dotted, path)
		case spec.Name.Name != "_":
			named[spec.Name.Name] = path
		}
	}
	return named, dotted
}

// dockerGuardFindings applies every rule to one parsed file and returns each violation with the declaration it sits
// in. It exempts nothing: the caller decides which findings an allow-list covers.
func dockerGuardFindings(fset *token.FileSet, file *ast.File) []guardFinding {
	named, dotted := guardImports(file)
	// resolve names the (path, name) pairs an expression can refer to: pkg.Name through a (possibly aliased)
	// import, or a bare Name through a dot import.
	resolve := func(e ast.Expr) []guardRef {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if path, ok := named[id.Name]; ok {
					return []guardRef{{path, x.Sel.Name}}
				}
			}
		case *ast.Ident:
			var out []guardRef
			for _, path := range dotted {
				out = append(out, guardRef{path, x.Name})
			}
			return out
		}
		return nil
	}
	// referenceRule classifies a reference that is a violation whatever surrounds it.
	referenceRule := func(r guardRef) string {
		switch r.path {
		case pkgNet:
			if strings.HasPrefix(r.name, "Dial") || r.name == "Dialer" {
				return ruleNetDial
			}
			if r.name == "FileConn" || r.name == "FilePacketConn" {
				return ruleFileConn
			}
		case pkgHTTP:
			if r.name == "DefaultTransport" || r.name == "DefaultClient" {
				return ruleHTTPDefault
			}
		case pkgUnix, pkgSyscall:
			if r.name == "Connect" {
				return ruleRawConnect
			}
		}
		return ""
	}
	isHTTPType := func(e ast.Expr) (string, bool) {
		for _, r := range resolve(e) {
			if r.path == pkgHTTP && (r.name == "Transport" || r.name == "Client") {
				return r.name, true
			}
		}
		return "", false
	}

	var out []guardFinding
	for _, decl := range file.Decls {
		name := guardDeclName(decl)
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			continue
		}
		add := func(rule string, pos token.Pos, text string) {
			out = append(out, guardFinding{rule: rule, decl: name, text: text, pos: pos})
		}
		// Identifiers that are the selected name of a selector are handled with the selector, not as bare names.
		selected := map[*ast.Ident]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				selected[sel.Sel] = true
			}
			return true
		})
		ast.Inspect(decl, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "dockerDialer" {
					add(ruleDockerDialer, x.Pos(), "references dockerDialer outside the client constructor")
				}
				if !selected[x] {
					for _, r := range resolve(x) {
						if rule := referenceRule(r); rule != "" {
							add(rule, x.Pos(), "uses "+r.path+"."+r.name+" through a dot import")
						}
					}
				}
			case *ast.SelectorExpr:
				for _, r := range resolve(x) {
					if rule := referenceRule(r); rule != "" {
						add(rule, x.Pos(), "uses "+r.path+"."+r.name)
					}
				}
				if x.Sel.Name == "DialContext" || x.Sel.Name == "Dial" {
					add(ruleMethodDial, x.Pos(), "calls ."+x.Sel.Name)
				}
			case *ast.CompositeLit:
				if x.Type != nil {
					if typ, ok := isHTTPType(x.Type); ok {
						add(ruleHTTPLiteral, x.Pos(), "builds its own http."+typ)
					}
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
					if typ, ok := isHTTPType(x.Args[0]); ok {
						add(ruleHTTPNew, x.Pos(), "new(http."+typ+")")
					}
				}
			}
			return true
		})
	}
	return out
}

func guardDeclName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.ValueSpec:
				return s.Names[0].Name
			case *ast.TypeSpec:
				return s.Name.Name
			}
		}
	}
	return ""
}
