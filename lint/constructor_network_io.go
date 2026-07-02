package main

import (
	"go/ast"
	"go/types"

	"github.com/dgageot/rubocop-go/cop"
)

// ConstructorNetworkIO enforces that constructors do not perform network I/O.
//
// Constructors should assemble state and return it. Dialing, listening, or
// issuing HTTP requests from New* hides network side effects before the caller
// can decide when to connect, arrange cancellation, or surface failures from an
// explicit operation.
//
// Detection is intentionally low-noise: only direct calls to selected package
// functions in net and net/http are flagged. Method calls such as .Do and
// .Accept are out of scope for now.
//
// Calls inside nested function literals are ignored unless the literal is
// immediately invoked as part of constructor execution.
//
// Annotate an intentional case with //rubocop:disable Lint/ConstructorNetworkIO.
var ConstructorNetworkIO = &cop.Func{
	Meta: cop.Meta{
		Name:        "Lint/ConstructorNetworkIO",
		Description: "constructors (New*) must not perform network I/O",
		Severity:    cop.Error,
	},
	Types: true,
	Run: func(p *cop.Pass) {
		p.ForEachFunc(func(fn *ast.FuncDecl) {
			if !isConstructor(fn) || fn.Body == nil {
				return
			}
			forEachConstructionCallExpr(fn.Body, func(call *ast.CallExpr) {
				pkg, name, ok := networkIOCall(p, call)
				if !ok {
					return
				}
				p.Reportf(call,
					"constructor %s calls %s.%s; move network I/O out of New into Start/Connect or the first request path",
					fn.Name.Name, pkg, name)
			})
		})
	},
}

func networkIOCall(p *cop.Pass, call *ast.CallExpr) (string, string, bool) {
	if pkg, name, ok := networkIOFuncName(calleeObject(p.Info, call)); ok {
		return pkg, name, true
	}

	if name, ok := cop.CallTo(call, "net", "Dial", "DialTimeout", "Listen", "ListenPacket", "ListenTCP", "ListenUDP", "ListenUnix"); ok {
		return "net", name, true
	}
	if name, ok := cop.CallTo(call, "http", "Get", "Head", "Post", "PostForm"); ok {
		return "http", name, true
	}
	return "", "", false
}

func networkIOFuncName(obj types.Object) (string, string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok {
		return "", "", false
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return "", "", false
	}
	name := fn.Name()
	switch pkg.Path() {
	case "net":
		switch name {
		case "Dial", "DialTimeout", "Listen", "ListenPacket", "ListenTCP", "ListenUDP", "ListenUnix":
			return "net", name, true
		}
	case "net/http":
		switch name {
		case "Get", "Head", "Post", "PostForm":
			return "http", name, true
		}
	}
	return "", "", false
}
