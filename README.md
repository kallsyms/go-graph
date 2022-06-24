# go-graph

Type-aware _everything_ code search (for Go only (right now)).

## Examples

* `x` calls `y`
* `x` with type `T` is used in a call to `y` (e.g. `data uint8[]; binary.Read(..., &data)`)
* The result of a call to `x` is used as args in other functions any number of times, but eventually one result of those calls is used as an arg to function `y` (e.g. `foo := x(...); tmp1 := bar(x); tmp2 := baz(tmp1); z := y(tmp2)`


## Current sample queries

Dump all functions called:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR call IN OUTBOUND f Calls
FOR callee IN OUTBOUND call Callee
RETURN callee
```

Dump all statements:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR statement IN OUTBOUND f Statement
RETURN statement
```

Dump all call sites of a function:
```
FOR p IN package
FILTER p.SourceURL == "fmt"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "Println"
FOR callsite IN INBOUND f Callee
FOR statement IN OUTBOUND callsite CallSiteStatement
RETURN statement
```

Find all paths through a function:
```
FOR pkg IN package
FILTER pkg.SourceURL == "code.gitea.io/gitea"
FOR func IN OUTBOUND pkg Functions
FILTER func.Name == "formatBuiltWith"
FOR firststatement IN OUTBOUND func FirstStatement
FOR v, e, path IN 1..100 OUTBOUND firststatement Next
PRUNE v.isBackEdge == true
FILTER LENGTH(FOR n IN OUTBOUND v Next RETURN n) == 0
RETURN {pkg, func, vertices: CONCAT_SEPARATOR(" -> ", FOR s IN path.vertices RETURN s.Text)}
```

Dump all variables referenced in a function:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR statement IN OUTBOUND f Statement
FOR variable IN OUTBOUND statement References
RETURN DISTINCT variable
```

Find all uses of `encoding/binary.Read` which pass a pointer to a list (causing a slow `reflect` path to be used):
```
FOR p IN package
FILTER p.SourceURL == "encoding/binary"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "Read"
FOR callsite IN INBOUND f Callee
FOR statement IN OUTBOUND callsite CallSiteStatement
FOR var IN OUTBOUND statement References
FILTER STARTS_WITH(var.Type, "[]")
FILTER CONTAINS(statement.Text, CONCAT("&", var.Name))
FOR callfunc in INBOUND statement Statement
FOR callpkg in INBOUND callfunc Functions
RETURN {package: callpkg.SourceURL, file: statement.File, text: statement.Text, var: var.Name, type: var.Type}
```

Find all uses of `crypto/rsa.GenerateKey`, where the result flows through 0 or more intermediary variables to reach a `pem.Encode` call:
```
// find calls to crypto/rsa.GenerateKey
FOR p IN package
FILTER p.SourceURL == "crypto/rsa"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "GenerateKey"
FOR call IN INBOUND f Callee

// Walk def->ref->def->ref->... until we reach a statement with an interesting call
FOR srccallstmt IN OUTBOUND call CallSiteStatement

// v "alternates" between being a variable and being a statement
FOR v, e, path IN 1..5 OUTBOUND srccallstmt Assigns, INBOUND References
    PRUNE CONTAINS(v.Text, "Encode")
    OPTIONS {uniqueVertices: "path"}
    // ensure the "reference" if this is a statement is not actually an assignment
    FILTER LENGTH(
        FOR checkassign IN OUTBOUND v Assigns
        FILTER path.vertices[*] ANY == checkassign
        RETURN checkassign
    ) == 0
    FILTER CONTAINS(v.Text, "Encode")
    RETURN path
```

# Comparison vs semgrep

## semgrep
Rule definition

```
rules:
- id: untitled_rule
  pattern: |
      binary.Read(..., &($X : []$Y))
  message: Semgrep found a match
  languages: [go]
  severity: WARNING
```

```
$ time semgrep --metrics=off -c /tmp/thing.yaml --verbose
...
====[ BEGIN error trace ]====
Raised at Stdlib__map.Make.find in file "map.ml", line 137, characters 10-25
Called from Sexplib0__Sexp_conv.Exn_converter.find_auto in file "src/sexp_conv.ml", line 156, characters 10-37
=====[ END error trace ]=====
...
3374.64s user 505.06s system 143% cpu 44:59.79 total
```
Can't analyze all repos at once (maybe because it doesn't do subdirectory .gitignores?)

one at a time:
run time: XXXX

```
$ time for d in *; do semgrep --metrics=off -c /tmp/thing.yaml $d; done
...
8210.36s user 1246.15s system 115% cpu 2:16:59.63 total
```

## This
Ingest time: ~11 hours
Run time: ~20s

```
FOR p IN package
FILTER p.SourceURL == "encoding/binary"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "Read"
FOR callsite IN INBOUND f Callee
FOR statement IN OUTBOUND callsite CallSiteStatement
FOR var IN OUTBOUND statement References
FILTER STARTS_WITH(var.Type, "[]")
FILTER CONTAINS(statement.Text, CONCAT("&", var.Name))
FOR callfunc in INBOUND statement Statement
FOR callpkg in INBOUND callfunc Functions
RETURN {package: callpkg.SourceURL, file: statement.File, text: statement.Text, var: var.Name, type: var.Type}

=> 628 elements
```
