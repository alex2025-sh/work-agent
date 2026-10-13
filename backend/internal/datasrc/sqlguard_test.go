package datasrc

import (
	"testing"

	"bywork/internal/expr"
)

func TestCompileBindsValues(t *testing.T) {
	q, args, err := Compile("SELECT * FROM t WHERE a = {{x}} AND b IN ({{y}}, 2) AND c >= {{z}}", expr.Vars{"x": "v", "y": 1, "z": 3})
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM t WHERE a = ? AND b IN (?, 2) AND c >= ?" || len(args) != 3 || args[0] != "v" {
		t.Fatalf("q=%s args=%v", q, args)
	}
}

func TestCompileRejects(t *testing.T) {
	bad := []string{
		"SELECT * FROM {{table}}",
		"SELECT * FROM t ORDER BY {{col}}",
		"SELECT * FROM t WHERE a = '{{x}}'",
		"DELETE FROM t",
		"SELECT 1; DROP TABLE t",
		"SELECT * FROM t FOR UPDATE",
		"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x",
		"UPDATE t SET a = 1",
	}
	for _, s := range bad {
		if _, _, err := Compile(s, expr.Vars{}); err == nil {
			t.Errorf("expected rejection: %s", s)
		}
	}
	if _, _, err := Compile("SELECT 'a;b' AS x FROM t -- trailing ; comment", expr.Vars{}); err != nil {
		t.Errorf("quoted semicolon should pass: %v", err)
	}
}
