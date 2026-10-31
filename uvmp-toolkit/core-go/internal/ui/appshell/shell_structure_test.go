package appshell

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionButtonsUseAppButtonWithSemanticIcons(t *testing.T) {
	directory := "."
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取 appshell 源码目录：%v", err)
	}
	files := token.NewFileSet()
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				function := ""
				switch fun := node.Fun.(type) {
				case *ast.Ident:
					function = fun.Name
				case *ast.SelectorExpr:
					packageName, _ := fun.X.(*ast.Ident)
					if packageName != nil && packageName.Name == "widget" {
						function = "widget." + fun.Sel.Name
					}
				}
				switch function {
				case "widget.NewButton", "widget.NewButtonWithIcon", "newMotionButton", "newMotionButtonWithIcon":
					violations = append(violations, name+": "+function+" 应迁移到 newAppButton/newAppIconButton")
				case "newAppButton", "newAppIconButton":
					iconIndex := 1
					if len(node.Args) <= iconIndex || emptyIconLiteral(node.Args[iconIndex]) {
						violations = append(violations, name+": "+function+" 必须登记非空语义图标名")
					}
				}
			case *ast.CompositeLit:
				if violation := compositeButtonIconViolation(node); violation != "" {
					violations = append(violations, name+": "+violation)
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("生产按钮结构门禁未通过：\n%s", strings.Join(violations, "\n"))
	}
}

func emptyIconLiteral(expression ast.Expr) bool {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err != nil || strings.TrimSpace(value) == ""
}

func compositeButtonIconViolation(literal *ast.CompositeLit) string {
	switch buttonCompositeType(literal.Type) {
	case "widget.Button":
		icon, ok := compositeField(literal, "Icon")
		if !ok || nilLiteral(icon) {
			return "widget.Button 复合字面量必须设置非 nil Icon"
		}
	case "appButton":
		button, ok := compositeField(literal, "Button")
		if !ok || nilLiteral(button) {
			return "appButton 复合字面量必须嵌入带非 nil Icon 的 widget.Button"
		}
	}
	return ""
}

func buttonCompositeType(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		if expression.Name == "appButton" {
			return "appButton"
		}
	case *ast.SelectorExpr:
		packageName, _ := expression.X.(*ast.Ident)
		if packageName != nil && packageName.Name == "widget" && expression.Sel.Name == "Button" {
			return "widget.Button"
		}
	}
	return ""
}

func compositeField(literal *ast.CompositeLit, name string) (ast.Expr, bool) {
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if ok && key.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

func nilLiteral(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == "nil"
}

func TestButtonStructureAuditRejectsCompositeLiteralWithoutIcon(t *testing.T) {
	violations := compositeLiteralViolations(t, `package appshell
var _ = &widget.Button{Text: "无图标"}
var _ = &widget.Button{Icon: nil}
var _ = &appButton{Button: &widget.Button{Text: "嵌套无图标"}}
var _ = &appButton{}
var _ = &appButton{Button: nil}
`)
	if len(violations) != 5 {
		t.Fatalf("缺失或 nil 图标的复合字面量应全部拦截：%v", violations)
	}
}

func TestButtonStructureAuditAllowsDynamicIconExpressions(t *testing.T) {
	violations := compositeLiteralViolations(t, `package appshell
var resource fyne.Resource
var embedded *widget.Button
var iconName = "download"
var _ = newAppButton("保存", iconName, widget.LowImportance, nil)
var _ = newAppIconButton("删除", iconName, widget.LowImportance, nil)
var _ = &widget.Button{Icon: resource}
var _ = &appButton{Button: embedded}
var _ = &appButton{Button: &widget.Button{Icon: lineIcon(iconName, nil)}}
`)
	if len(violations) != 0 {
		t.Fatalf("动态非空图标表达式不得误报：%v", violations)
	}
}

func compositeLiteralViolations(t *testing.T, source string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "inline.go", source, 0)
	if err != nil {
		t.Fatalf("解析门禁夹具：%v", err)
	}
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if violation := compositeButtonIconViolation(literal); violation != "" {
			violations = append(violations, violation)
		}
		return true
	})
	return violations
}
