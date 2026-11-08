package chargersocket

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lorenzodonini/ocpp-go/ws"
)

// The write pump reports a server-side close to the handler of the moment.
// When ocppj.Client.Stop installs its own handler after that, Stop must
// still hear of the disconnect, once, or it waits forever.
func TestSocketPassesAMissedDisconnectToStop(t *testing.T) {
	s := New(ws.NewClient())
	var first, second int
	s.SetDisconnectedHandler(func(error) { first++ })
	s.disconnected(errors.New("server closed")) // as the write pump does
	s.SetDisconnectedHandler(func(error) { second++ })
	s.Stop()
	s.Stop()
	if first != 1 || second != 1 {
		t.Fatalf("disconnect reached the first handler %d times and Stop's %d times, want once each", first, second)
	}
	if err := s.Write([]byte("[]")); err == nil {
		t.Fatal("a stopped charger still wrote")
	}
}

// Every charger in the module dials through a Socket. The races above need
// a few instructions of bad luck, so a charger built on the plain client
// passes its own tests and fails CI weeks later; this finds it at once.
func TestEveryChargerDialsThroughASocket(t *testing.T) {
	root := moduleRoot(t)
	type client struct{ pkg, ctor string }
	clients := map[string]client{
		"github.com/lorenzodonini/ocpp-go/ocpp1.6":   {"ocpp16", "NewChargePoint"},
		"github.com/lorenzodonini/ocpp-go/ocpp2.0.1": {"ocpp2", "NewChargingStation"},
	}
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			// The go tool skips the same directories, and a directory with
			// its own go.mod is another module.
			name := d.Name()
			if path == here || name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // the build reports a file that does not parse
		}
		ctors := map[string]string{} // local package name, "." for a dot import -> constructor
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			c, ok := clients[p]
			if !ok {
				continue
			}
			name := c.pkg
			if imp.Name != nil {
				name = imp.Name.Name
			}
			ctors[name] = c.ctor
		}
		if len(ctors) == 0 {
			return nil
		}
		report := func(pos token.Pos) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel+":"+strconv.Itoa(fset.Position(pos).Line))
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := n.X.(*ast.Ident); ok && ctors[pkg.Name] == n.Sel.Name {
					report(n.Pos())
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == ctors["."] {
					report(n.Pos())
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("chargers built on ocpp-go's plain client, use ChargePoint16 or ChargingStation201:\n%s", strings.Join(found, "\n"))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this package")
		}
		dir = parent
	}
}
